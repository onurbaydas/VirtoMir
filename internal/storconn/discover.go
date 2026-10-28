package storconn

import (
	"context"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Found is storage that can be connected as a pool.
type Found struct {
	Kind   string `json:"kind"`
	Server string `json:"server,omitempty"` // address (iSCSI, NFS, SMB)
	Host   string `json:"host,omitempty"`   // its DNS name, when it has one
	// Name: the target IQN, export path or share; for FC the LUN's device.
	Name string `json:"name"`
	// Portals: every address:port an iSCSI target answers on (more than one: multipath).
	Portals   []string `json:"portals,omitempty"`
	SizeBytes uint64   `json:"sizeBytes,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	// NeedsAuth: the server wants a user name and password (to list its shares, or for this
	// target); Name is empty when even the list needs them.
	NeedsAuth bool `json:"needsAuth,omitempty"`
	// Error: the server answered on the port but could not be asked (shown, not hidden).
	Error string `json:"error,omitempty"`
	// Pool: the pool it already is on this host.
	Pool string `json:"pool,omitempty"`
	// Adapter: an FC port of this host (Name is its WWPN, to zone LUNs to), not storage.
	Adapter bool `json:"adapter,omitempty"`
}

// SANFound lists this host's FC ports and the empty SAN LUNs (used: paths a pool already
// has). LUNs mapped since boot are found first, and multipath is turned on when a LUN shows
// on more than one path.
func SANFound(ctx context.Context, r Runner, used func(path string) bool) []Found {
	Rescan(ctx, r)
	out := []Found{}
	if _, err := EnableMultipath(ctx, r); err != nil {
		out = append(out, Found{Kind: KindLUN, Error: err.Error()})
	}
	for _, a := range Adapters() {
		f := Found{Kind: KindFC, Name: a.WWPN, Detail: strings.TrimSpace(a.Host + " · " + a.State + " · " + a.Speed), Adapter: true}
		if a.State != "Online" {
			f.Error = "the port is " + a.State + ": check its cable and the switch port"
		}
		out = append(out, f)
	}
	luns, _ := LUNs(ctx, r)
	for _, l := range luns {
		if !l.Empty || used(l.Path) {
			continue
		}
		kind := KindLUN
		if l.Tran == "fc" {
			kind = KindFC
		}
		f := Found{Kind: kind, Name: l.Path, SizeBytes: l.SizeBytes}
		if l.Paths > 1 {
			f.Detail = fmt.Sprintf("%d paths", l.Paths)
		}
		out = append(out, f)
	}
	return out
}

// Credentials for a server that asks for them.
type Credentials struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// Discover asks every server what it offers. creds: per server address, for servers that
// refused to list without them.
func Discover(ctx context.Context, r Runner, servers map[string][]string, creds map[string]Credentials) []Found {
	kinds := map[string]bool{}
	for _, ks := range servers {
		for _, k := range ks {
			kinds[k] = true
		}
	}
	ensured := map[string]error{}
	for k := range kinds {
		ensured[k] = Ensure(ctx, r, k)
	}

	var mu sync.Mutex
	out := []Found{}
	var wg sync.WaitGroup
	for server, ks := range servers {
		for _, k := range ks {
			wg.Add(1)
			go func(server, kind string) {
				defer wg.Done()
				var fs []Found
				if err := ensured[kind]; err != nil {
					fs = []Found{{Kind: kind, Server: server, Error: err.Error()}}
				} else {
					fs = ask(ctx, r, kind, server, creds[server])
				}
				name := hostName(ctx, server)
				for i := range fs {
					fs[i].Host = name
				}
				mu.Lock()
				out = append(out, fs...)
				mu.Unlock()
			}(server, k)
		}
	}
	wg.Wait()
	slices.SortFunc(out, func(a, b Found) int {
		return strings.Compare(a.Kind+" "+a.Server+" "+a.Name, b.Kind+" "+b.Server+" "+b.Name)
	})
	return out
}

func ask(ctx context.Context, r Runner, kind, server string, c Credentials) []Found {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	switch kind {
	case KindISCSI:
		out, err := r.Run(ctx, "iscsiadm", "-m", "discovery", "-t", "sendtargets", "-p", net.JoinHostPort(server, "3260"))
		if err != nil {
			if authError(err.Error()) {
				return []Found{{Kind: kind, Server: server, NeedsAuth: true}}
			}
			return []Found{{Kind: kind, Server: server, Error: err.Error()}}
		}
		return parseSendTargets(server, string(out))
	case KindNFS:
		out, err := r.Run(ctx, "showmount", "-e", "--no-headers", server)
		if err != nil {
			// NFSv4-only servers run no mount daemon to ask; their root is exported.
			return []Found{{Kind: kind, Server: server, Name: "/", Detail: "NFSv4"}}
		}
		return parseShowmount(server, string(out))
	case KindSMB:
		args := []string{"-L", "//" + server, "-g"}
		if c.Username == "" {
			args = append(args, "-N")
		} else {
			auth, err := authFile(c)
			if err != nil {
				return []Found{{Kind: kind, Server: server, Error: err.Error()}}
			}
			defer os.Remove(auth)
			args = append(args, "-A", auth)
		}
		out, err := r.Run(ctx, "smbclient", args...)
		if err != nil {
			if authError(err.Error() + string(out)) { // smbclient reports on standard output
				return []Found{{Kind: kind, Server: server, NeedsAuth: true}}
			}
			return []Found{{Kind: kind, Server: server, Error: err.Error()}}
		}
		fs := parseSMBList(server, string(out))
		for i := range fs {
			fs[i].NeedsAuth = c.Username != ""
		}
		return fs
	}
	return nil
}

func authError(s string) bool {
	for _, m := range []string{"ACCESS_DENIED", "LOGON_FAILURE", "authenticat", "authoriz", "rejected"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// parseSendTargets reads iscsiadm's "address:port,tpgt iqn" lines: one target, its portals.
func parseSendTargets(server, out string) []Found {
	var fs []Found
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		portal, _, _ := strings.Cut(f[0], ",")
		i := slices.IndexFunc(fs, func(x Found) bool { return x.Name == f[1] })
		if i < 0 {
			fs = append(fs, Found{Kind: KindISCSI, Server: server, Name: f[1]})
			i = len(fs) - 1
		}
		if !slices.Contains(fs[i].Portals, portal) {
			fs[i].Portals = append(fs[i].Portals, portal)
		}
	}
	return fs
}

// parseShowmount reads "showmount -e --no-headers": an export path and who may mount it.
func parseShowmount(server, out string) []Found {
	var fs []Found
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
			continue
		}
		fs = append(fs, Found{Kind: KindNFS, Server: server, Name: f[0], Detail: strings.Join(f[1:], " ")})
	}
	return fs
}

// parseSMBList reads "smbclient -L -g": "Disk|name|comment" lines; printers, IPC$ and the
// hidden administrative shares are left out.
func parseSMBList(server, out string) []Found {
	var fs []Found
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(f) < 2 || f[0] != "Disk" || strings.HasSuffix(f[1], "$") {
			continue
		}
		d := Found{Kind: KindSMB, Server: server, Name: f[1]}
		if len(f) == 3 {
			d.Detail = f[2]
		}
		fs = append(fs, d)
	}
	return fs
}

// authFile writes credentials for smbclient and mount.cifs to a file only root can read
// (never on a command line, where every user sees it).
func authFile(c Credentials) (string, error) {
	f, err := os.CreateTemp("", "virtomir-smb-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(credText(c)); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func credText(c Credentials) string {
	user, domain := c.Username, ""
	if d, u, ok := strings.Cut(c.Username, `\`); ok {
		domain, user = d, u
	}
	s := "username=" + user + "\npassword=" + c.Password + "\n"
	if domain != "" {
		s += "domain=" + domain + "\n"
	}
	return s
}

// hostName is a server's DNS name, if it has one.
func hostName(ctx context.Context, addr string) string {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, addr)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}
