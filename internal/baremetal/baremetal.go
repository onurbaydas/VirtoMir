// Package baremetal installs new hosts over the network (D17, ADR-0077): a machine that
// boots from the network gets an iPXE script from VirtoMir, the Debian 13 installer and a
// preseed that installs VirtoMir from its signed package repository; on its first boot the
// host fetches a join code and joins the cluster. Its BMC (Redfish or IPMI) can switch it
// on to boot from the network once.
package baremetal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// Host states.
const (
	StateRegistered = "registered" // known, not being installed
	StateInstalling = "installing" // its next network boot installs it
	StateInstalled  = "installed"  // the installer finished; it boots from its disk
	StateJoining    = "joining"    // it fetched its join code
	StateJoined     = "joined"     // a member of the cluster
	StateFailed     = "failed"
)

// Network boot modes.
const (
	// ModeExternal: the network's DHCP server points machines at VirtoMir (next-server and
	// filename); VirtoMir only serves the boot files.
	ModeExternal = "external"
	// ModeProxy: VirtoMir answers PXE requests itself as a proxy DHCP server (dnsmasq), next
	// to the network's DHCP server, only for the machines it is installing.
	ModeProxy = "proxy"
)

// Errors for the API.
var (
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("exists")
	ErrBusy     = errors.New("busy")
)

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...)
}

// BMC is a host's out-of-band management controller.
type BMC struct {
	Driver      string `json:"driver"`             // "" (none), "redfish" or "ipmi"
	Endpoint    string `json:"endpoint,omitempty"` // host, host:port or https://host
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"` // write-only in the API
	InsecureTLS bool   `json:"insecureTls,omitempty"`
}

// Static is a fixed address for the installed host (the installer itself uses DHCP).
type Static struct {
	Address string   `json:"address"` // 10.0.0.21/24
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
}

// Event is one line of a host's log.
type Event struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
	Error   bool      `json:"error,omitempty"`
}

// Host is a machine VirtoMir installs.
type Host struct {
	ID   string `json:"id"`
	Name string `json:"name"` // its hostname, and its name in the cluster
	MAC  string `json:"mac"`  // of the network card it boots from
	BMC  BMC    `json:"bmc"`
	// Disk the system goes on ("" = the first disk the installer finds). Everything on it
	// is erased.
	Disk    string   `json:"disk,omitempty"`
	Static  *Static  `json:"static,omitempty"`
	SSHKeys []string `json:"sshKeys,omitempty"`
	// PasswordHash is the crypt hash of the password of the user virtomir (admin through
	// PAM). The API takes a password and never shows it.
	PasswordHash string    `json:"passwordHash,omitempty"`
	State        string    `json:"state"`
	Message      string    `json:"message,omitempty"`
	Token        string    `json:"token,omitempty"` // in boot URLs during one install; never in the API
	Events       []Event   `json:"events"`
	Created      time.Time `json:"created"`
	Updated      time.Time `json:"updated"`
}

// Public is the host as the API shows it.
func (h Host) Public() Host {
	h.Token, h.PasswordHash = "", ""
	h.BMC.Password = ""
	h.Events = append([]Event(nil), h.Events...)
	return h
}

// Settings say where and how VirtoMir serves network boots.
type Settings struct {
	Enabled bool `json:"enabled"`
	// Address is this host's IP on the network the machines boot in; Port its plain HTTP
	// port for boot files (iPXE does not trust self-signed certificates).
	Address string `json:"address"`
	Port    int    `json:"port"`
	Mode    string `json:"mode"`
	// Interface carries the proxy DHCP answers (ModeProxy).
	Interface string `json:"interface,omitempty"`
	// Mirror is the Debian mirror the installer uses; Repo the VirtoMir apt repository.
	Mirror string `json:"mirror"`
	Repo   string `json:"repo"`
}

// Defaults.
const (
	DefaultPort   = 8424
	DefaultMirror = "deb.debian.org"
	DefaultRepo   = "https://onurbaydas.github.io/VirtoMir/apt stable main"
)

var (
	nameRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	diskRE  = regexp.MustCompile(`^/dev/[A-Za-z0-9/_.:-]+$`)
	ifaceRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)
	repoRE  = regexp.MustCompile(`^https?://[^\s"'\\]+ [A-Za-z0-9._-]+ [A-Za-z0-9._ -]+$`)
	keyRE   = regexp.MustCompile(`^(ssh-|ecdsa-|sk-)[^\n\r'"\\]*$`)
	hostRE  = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
)

// Normalize fills defaults and checks settings.
func (s *Settings) Normalize() error {
	if s.Port == 0 {
		s.Port = DefaultPort
	}
	if s.Mode == "" {
		s.Mode = ModeExternal
	}
	if s.Mirror == "" {
		s.Mirror = DefaultMirror
	}
	if s.Repo == "" {
		s.Repo = DefaultRepo
	}
	switch {
	case s.Port < 1 || s.Port > 65535:
		return invalidf("the boot port is 1 to 65535")
	case s.Mode != ModeExternal && s.Mode != ModeProxy:
		return invalidf("the mode is external or proxy")
	case !hostRE.MatchString(s.Mirror):
		return invalidf("the Debian mirror is a host name like deb.debian.org")
	case !repoRE.MatchString(s.Repo):
		return invalidf(`the repository is "URL suite component", like %s`, DefaultRepo)
	}
	if !s.Enabled {
		return nil
	}
	if a, err := netip.ParseAddr(s.Address); err != nil || !a.Is4() {
		return invalidf("give this host's IPv4 address on the boot network")
	}
	if s.Mode == ModeProxy && !ifaceRE.MatchString(s.Interface) {
		return invalidf("proxy DHCP needs the network interface of the boot network")
	}
	return nil
}

// Base is the URL the machines fetch boot files from.
func (s Settings) Base() string {
	return fmt.Sprintf("http://%s:%d", s.Address, s.Port)
}

// NormalizeMAC is a MAC address in lower case with colons, or "".
func NormalizeMAC(s string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return ""
	}
	return hw.String()
}

// Validate checks a host (with its password already hashed).
func (h *Host) Validate() error {
	h.Name = strings.ToLower(strings.TrimSpace(h.Name))
	if !nameRE.MatchString(h.Name) {
		return invalidf("a host name is up to 63 lower-case letters, digits and dashes")
	}
	if h.MAC = NormalizeMAC(h.MAC); h.MAC == "" {
		return invalidf("give the MAC address of the network card the machine boots from")
	}
	if h.Disk != "" && !diskRE.MatchString(h.Disk) {
		return invalidf("the disk is a device like /dev/sda or /dev/nvme0n1")
	}
	switch h.BMC.Driver {
	case "":
	case "redfish", "ipmi":
		if strings.TrimSpace(h.BMC.Endpoint) == "" || strings.ContainsAny(h.BMC.Endpoint, " \n") {
			return invalidf("give the BMC's address")
		}
	default:
		return invalidf("the BMC is redfish, ipmi or none")
	}
	if st := h.Static; st != nil {
		p, err := netip.ParsePrefix(st.Address)
		if err != nil || !p.Addr().Is4() {
			return invalidf("the fixed address is IPv4 with its prefix, like 10.0.0.21/24")
		}
		if st.Gateway != "" {
			if g, err := netip.ParseAddr(st.Gateway); err != nil || !p.Masked().Contains(g) {
				return invalidf("the gateway %q is not in %s", st.Gateway, p.Masked())
			}
		}
		for _, d := range st.DNS {
			if _, err := netip.ParseAddr(d); err != nil {
				return invalidf("the DNS server %q is not an IP address", d)
			}
		}
	}
	keys := h.SSHKeys[:0]
	for _, k := range h.SSHKeys {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if !keyRE.MatchString(k) {
			return invalidf("that does not look like an SSH public key: %.30s…", k)
		}
		keys = append(keys, k)
	}
	h.SSHKeys = keys
	if h.PasswordHash == "" && len(h.SSHKeys) == 0 {
		return invalidf("set a password or an SSH key for the user virtomir, otherwise nobody can sign in to the host")
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
