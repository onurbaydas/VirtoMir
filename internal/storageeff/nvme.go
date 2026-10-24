package storageeff

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// NVMe over TCP (experimental, not tested on hardware): subsystems on a storage target are
// discovered and connected with nvme-cli; their namespaces appear as /dev/nvmeXnY and
// become storage like any empty disk (an LVM pool on them).

// Target is an NVMe/TCP subsystem at a portal.
type Target struct {
	Portal string `json:"portal"`         // the target's IP address
	Port   int    `json:"port,omitempty"` // default 4420
	NQN    string `json:"nqn"`
}

var nqnRE = regexp.MustCompile(`^nqn\.[0-9]{4}-[0-9]{2}\.[A-Za-z0-9.-]+(:[A-Za-z0-9._:-]+)?$`)

func (t *Target) normalize() error {
	if _, err := netip.ParseAddr(t.Portal); err != nil {
		return invalidf("the portal %q is not an IP address", t.Portal)
	}
	if t.Port == 0 {
		t.Port = 4420
	}
	if t.Port < 1 || t.Port > 65535 {
		return invalidf("the port is 1 to 65535")
	}
	if t.NQN != "" && !nqnRE.MatchString(t.NQN) {
		return invalidf("%q is not an NVMe qualified name (nqn.2026-01.org.example:disk1)", t.NQN)
	}
	return nil
}

// Discover lists the NVMe subsystems a portal offers.
func Discover(ctx context.Context, r Runner, portal string, port int) ([]Target, error) {
	t := Target{Portal: portal, Port: port}
	if err := t.normalize(); err != nil {
		return nil, err
	}
	out, err := r.Run(ctx, "nvme", "discover", "-t", "tcp", "-a", t.Portal, "-s", strconv.Itoa(t.Port), "-o", "json")
	if err != nil {
		return nil, missing(err, "nvme-cli")
	}
	var doc struct {
		Records []struct {
			SubType string `json:"subtype"`
			TrType  string `json:"trtype"`
			TrAddr  string `json:"traddr"`
			TrSvcID string `json:"trsvcid"`
			SubNQN  string `json:"subnqn"`
		} `json:"records"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("read nvme discover: %w", err)
	}
	list := []Target{}
	for _, rec := range doc.Records {
		if rec.TrType != "tcp" || !strings.Contains(rec.SubType, "nvme subsystem") {
			continue // discovery controllers point at themselves
		}
		p, _ := strconv.Atoi(rec.TrSvcID)
		list = append(list, Target{Portal: rec.TrAddr, Port: p, NQN: rec.SubNQN})
	}
	return list, nil
}

// Connect connects a subsystem and keeps it to connect again at start.
func Connect(ctx context.Context, r Runner, store *Store, t Target) error {
	if err := t.normalize(); err != nil {
		return err
	}
	if t.NQN == "" {
		return invalidf("name the subsystem (NQN) to connect")
	}
	if _, err := r.Run(ctx, "nvme", "connect", "-t", "tcp", "-a", t.Portal, "-s", strconv.Itoa(t.Port), "-n", t.NQN); err != nil {
		return missing(err, "nvme-cli")
	}
	return store.setNVMe(func(list []Target) []Target {
		list = slices.DeleteFunc(list, func(x Target) bool { return x.NQN == t.NQN })
		return append(list, t)
	})
}

// Disconnect disconnects a subsystem; its namespaces disappear from this host.
func Disconnect(ctx context.Context, r Runner, store *Store, nqn string) error {
	if !nqnRE.MatchString(nqn) {
		return invalidf("%q is not an NVMe qualified name", nqn)
	}
	if _, err := r.Run(ctx, "nvme", "disconnect", "-n", nqn); err != nil {
		return missing(err, "nvme-cli")
	}
	return store.setNVMe(func(list []Target) []Target {
		return slices.DeleteFunc(list, func(x Target) bool { return x.NQN == nqn })
	})
}

// Namespace is a block device of a connected subsystem.
type Namespace struct {
	Device    string `json:"device"` // /dev/nvme0n1
	NSID      int    `json:"nsid"`
	SizeBytes uint64 `json:"sizeBytes"`
	Model     string `json:"model"`
	Serial    string `json:"serial"`
}

// Subsystem is a connected NVMe subsystem with its paths and namespaces.
type Subsystem struct {
	NQN        string      `json:"nqn"`
	Transport  string      `json:"transport"`         // tcp, pcie, …
	Address    string      `json:"address,omitempty"` // traddr=…,trsvcid=…
	State      string      `json:"state"`             // live, connecting, …
	Namespaces []Namespace `json:"namespaces"`
	// Managed: VirtoMir connected it (and connects it again at start).
	Managed bool `json:"managed"`
}

var nsDevRE = regexp.MustCompile(`^/dev/nvme(\d+)n(\d+)$`)

// List lists the NVMe subsystems of this host (local PCIe drives too) and their
// namespaces. With native multipath a namespace is /dev/nvme<subsystem>n<ns>, without
// it /dev/nvme<controller>n<ns>; either number finds its subsystem.
func List(ctx context.Context, r Runner, store *Store) ([]Subsystem, error) {
	out, err := r.Run(ctx, "nvme", "list-subsys", "-o", "json")
	if err != nil {
		return nil, missing(err, "nvme-cli")
	}
	var hosts []struct {
		Subsystems []struct {
			Name  string `json:"Name"` // nvme-subsys0
			NQN   string `json:"NQN"`
			Paths []struct {
				Name      string `json:"Name"` // nvme0
				Transport string `json:"Transport"`
				Address   string `json:"Address"`
				State     string `json:"State"`
			} `json:"Paths"`
		} `json:"Subsystems"`
	}
	if err := json.Unmarshal(out, &hosts); err != nil {
		// nvme-cli 1.x prints one object instead of a list of hosts.
		var one struct {
			Subsystems json.RawMessage `json:"Subsystems"`
		}
		if json.Unmarshal(out, &one) != nil {
			return nil, fmt.Errorf("read nvme list-subsys: %w", err)
		}
		if err := json.Unmarshal([]byte(`[{"Subsystems":`+string(one.Subsystems)+`}]`), &hosts); err != nil {
			return nil, fmt.Errorf("read nvme list-subsys: %w", err)
		}
	}
	out, err = r.Run(ctx, "nvme", "list", "-o", "json")
	if err != nil {
		return nil, err
	}
	var devs struct {
		Devices []struct {
			NameSpace    int    `json:"NameSpace"`
			DevicePath   string `json:"DevicePath"`
			ModelNumber  string `json:"ModelNumber"`
			SerialNumber string `json:"SerialNumber"`
			PhysicalSize uint64 `json:"PhysicalSize"`
		} `json:"Devices"`
	}
	if err := json.Unmarshal(out, &devs); err != nil {
		return nil, fmt.Errorf("read nvme list: %w", err)
	}
	managed := map[string]bool{}
	for _, t := range store.NVMeTargets() {
		managed[t.NQN] = true
	}
	list := []Subsystem{}
	for _, h := range hosts {
		for _, ss := range h.Subsystems {
			sub := Subsystem{NQN: ss.NQN, Managed: managed[ss.NQN], Namespaces: []Namespace{}}
			owners := map[string]bool{strings.TrimPrefix(ss.Name, "nvme-subsys"): true}
			for _, p := range ss.Paths {
				owners[strings.TrimPrefix(p.Name, "nvme")] = true
				if sub.Transport == "" || p.State == "live" {
					sub.Transport, sub.Address, sub.State = p.Transport, p.Address, p.State
				}
			}
			for _, d := range devs.Devices {
				if m := nsDevRE.FindStringSubmatch(d.DevicePath); m != nil && owners[m[1]] {
					sub.Namespaces = append(sub.Namespaces, Namespace{Device: d.DevicePath, NSID: d.NameSpace, SizeBytes: d.PhysicalSize,
						Model: strings.TrimSpace(d.ModelNumber), Serial: strings.TrimSpace(d.SerialNumber)})
				}
			}
			list = append(list, sub)
		}
	}
	return list, nil
}

// Reconnect connects the subsystems VirtoMir connected before that are not connected now.
func Reconnect(ctx context.Context, r Runner, store *Store) []error {
	targets := store.NVMeTargets()
	if len(targets) == 0 {
		return nil
	}
	now, err := List(ctx, r, store)
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, t := range targets {
		if slices.ContainsFunc(now, func(s Subsystem) bool { return s.NQN == t.NQN && s.State == "live" }) {
			continue
		}
		if _, err := r.Run(ctx, "nvme", "connect", "-t", "tcp", "-a", t.Portal, "-s", strconv.Itoa(t.Port), "-n", t.NQN); err != nil {
			errs = append(errs, fmt.Errorf("reconnect %s: %w", t.NQN, err))
		}
	}
	return errs
}
