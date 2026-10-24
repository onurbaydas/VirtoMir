package physnet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SR-IOV (experimental, not tested on hardware): a capable NIC (the physical function)
// splits into virtual functions, each a PCI device a machine can own. VirtoMir sets how many
// VFs a NIC has (kept across reboots by a udev rule) and makes libvirt networks in hostdev
// mode on a PF: a machine on such a network gets a free VF of it.

// VF is one virtual function of a NIC.
type VF struct {
	Index  int    `json:"index"`
	PCI    string `json:"pci"`              // 0000:04:10.1
	Netdev string `json:"netdev,omitempty"` // only while the host's driver has it
	MAC    string `json:"mac,omitempty"`
}

// SRIOVDevice is an SR-IOV capable NIC.
type SRIOVDevice struct {
	Interface  string `json:"interface"`
	PCI        string `json:"pci"`
	Driver     string `json:"driver"`
	TotalVFs   int    `json:"totalVfs"`
	CurrentVFs int    `json:"currentVfs"`
	VFs        []VF   `json:"vfs"`
	// Persistent: a udev rule sets the VF count again at boot.
	Persistent bool `json:"persistent"`
}

// SRIOV reads the host's SR-IOV NICs from sysfs under root ("/" on a host) and the udev
// rules VirtoMir wrote in rulesDir.
type SRIOV struct {
	Root     string
	RulesDir string
}

// Host is the real host.
var Host = SRIOV{Root: "/", RulesDir: "/etc/udev/rules.d"}

var ifaceRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

func readInt(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

func linkBase(path string) string {
	t, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return filepath.Base(t)
}

func (s SRIOV) ruleFile(iface string) string {
	return filepath.Join(s.RulesDir, "70-virtomir-sriov-"+iface+".rules")
}

// Devices lists the SR-IOV capable NICs.
func (s SRIOV) Devices() ([]SRIOVDevice, error) {
	nets, err := filepath.Glob(filepath.Join(s.Root, "sys/class/net/*"))
	if err != nil {
		return nil, err
	}
	out := []SRIOVDevice{}
	for _, n := range nets {
		total, err := readInt(filepath.Join(n, "device/sriov_totalvfs"))
		if err != nil || total <= 0 {
			continue // no SR-IOV, or a VF itself
		}
		d := SRIOVDevice{Interface: filepath.Base(n), TotalVFs: total, PCI: linkBase(filepath.Join(n, "device")),
			Driver: linkBase(filepath.Join(n, "device/driver")), VFs: []VF{}}
		d.CurrentVFs, _ = readInt(filepath.Join(n, "device/sriov_numvfs"))
		links, _ := filepath.Glob(filepath.Join(n, "device/virtfn*"))
		for _, l := range links {
			i, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(l), "virtfn"))
			if err != nil {
				continue
			}
			vf := VF{Index: i, PCI: linkBase(l)}
			if devs, _ := filepath.Glob(filepath.Join(l, "net/*")); len(devs) > 0 {
				vf.Netdev = filepath.Base(devs[0])
				if mac, err := os.ReadFile(filepath.Join(devs[0], "address")); err == nil {
					vf.MAC = strings.TrimSpace(string(mac))
				}
			}
			d.VFs = append(d.VFs, vf)
		}
		sort.Slice(d.VFs, func(a, b int) bool { return d.VFs[a].Index < d.VFs[b].Index })
		if _, err := os.Stat(s.ruleFile(d.Interface)); err == nil {
			d.Persistent = true
		}
		out = append(out, d)
	}
	return out, nil
}

// SetVFs gives a NIC n virtual functions (0 removes them). The kernel only changes a
// non-zero count through 0, so machines using its VFs lose them: callers check first.
// With persistent, a udev rule sets the count again at boot.
func (s SRIOV) SetVFs(iface string, n int, persistent bool) error {
	if !ifaceRE.MatchString(iface) {
		return invalidf("%q is not a network interface name", iface)
	}
	dev := filepath.Join(s.Root, "sys/class/net", iface, "device")
	total, err := readInt(filepath.Join(dev, "sriov_totalvfs"))
	if err != nil {
		return fmt.Errorf("%w: %s does not support SR-IOV", ErrNotFound, iface)
	}
	if n < 0 || n > total {
		return invalidf("%s supports 0 to %d virtual functions", iface, total)
	}
	numvfs := filepath.Join(dev, "sriov_numvfs")
	cur, _ := readInt(numvfs)
	if cur != n {
		if cur > 0 && n > 0 {
			if err := os.WriteFile(numvfs, []byte("0"), 0o644); err != nil {
				return err
			}
		}
		if err := os.WriteFile(numvfs, []byte(strconv.Itoa(n)), 0o644); err != nil {
			return fmt.Errorf("set %d VFs on %s: %w", n, iface, err)
		}
	}
	rule := s.ruleFile(iface)
	if persistent && n > 0 {
		if err := os.MkdirAll(s.RulesDir, 0o755); err != nil {
			return err
		}
		text := fmt.Sprintf("# Written by VirtoMir: %d SR-IOV virtual functions on %s at boot.\nACTION==\"add\", SUBSYSTEM==\"net\", KERNEL==\"%s\", ATTR{device/sriov_numvfs}=\"%d\"\n", n, iface, iface, n)
		return os.WriteFile(rule, []byte(text), 0o644)
	}
	if err := os.Remove(rule); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
