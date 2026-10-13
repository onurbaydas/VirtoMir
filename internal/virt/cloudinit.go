package virt

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/kdomanski/iso9660" // BSD-2-Clause
	"libvirt.org/go/libvirtxml"
)

// CloudInit configures a cloud image on first boot (NoCloud datasource): user, password
// and/or SSH keys, hostname. The QEMU guest agent is always installed so VirtoMir can see
// the machine's IP addresses.
type CloudInit struct {
	Hostname string   `json:"hostname,omitempty"`
	User     string   `json:"user"`
	Password string   `json:"password,omitempty"`
	SSHKeys  []string `json:"sshKeys,omitempty"`
	Packages []string `json:"packages,omitempty"`
	// UserData replaces the generated #cloud-config entirely (power users).
	UserData string `json:"userData,omitempty"`
}

var (
	unixUserRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	hostnameRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	packageRE  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_:-]*$`)
)

func (ci *CloudInit) normalize(machine string) error {
	if ci.UserData != "" {
		if !strings.HasPrefix(strings.TrimSpace(ci.UserData), "#cloud-config") && !strings.HasPrefix(ci.UserData, "#!") {
			return invalidf("Custom user-data must start with #cloud-config (or #! for a script).")
		}
		return nil
	}
	if ci.Hostname == "" {
		ci.Hostname = strings.ToLower(strings.NewReplacer(".", "-", "_", "-", "+", "-").Replace(machine))
	}
	if !hostnameRE.MatchString(ci.Hostname) {
		return invalidf("Hostname %q is not valid: letters, digits and '-', up to 63 characters.", ci.Hostname)
	}
	if !unixUserRE.MatchString(ci.User) {
		return invalidf("User name %q is not valid: lowercase letters, digits, '_' or '-', starting with a letter.", ci.User)
	}
	keys := ci.SSHKeys[:0]
	for _, k := range ci.SSHKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !strings.HasPrefix(k, "ssh-") && !strings.HasPrefix(k, "ecdsa-") && !strings.HasPrefix(k, "sk-") {
			return invalidf("That does not look like an SSH public key: %.30s…", k)
		}
		keys = append(keys, k)
	}
	ci.SSHKeys = keys
	if ci.Password == "" && len(ci.SSHKeys) == 0 {
		return invalidf("Set a password or an SSH key, otherwise nobody can sign in to the machine.")
	}
	for _, p := range ci.Packages {
		if !packageRE.MatchString(p) {
			return invalidf("Package name %q is not valid.", p)
		}
	}
	return nil
}

// q quotes a value for YAML (a JSON string is a valid YAML scalar).
func q(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// userData renders the #cloud-config for a normalized spec.
func (ci *CloudInit) userData() string {
	if ci.UserData != "" {
		return ci.UserData
	}
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	fmt.Fprintf(&b, "hostname: %s\nfqdn: %s\nmanage_etc_hosts: true\n", q(ci.Hostname), q(ci.Hostname))
	b.WriteString("users:\n")
	fmt.Fprintf(&b, "  - name: %s\n    groups: [sudo, wheel, adm]\n    sudo: \"ALL=(ALL) NOPASSWD:ALL\"\n    shell: /bin/bash\n", q(ci.User))
	if ci.Password != "" {
		fmt.Fprintf(&b, "    lock_passwd: false\n    plain_text_passwd: %s\n", q(ci.Password))
	}
	if len(ci.SSHKeys) > 0 {
		b.WriteString("    ssh_authorized_keys:\n")
		for _, k := range ci.SSHKeys {
			fmt.Fprintf(&b, "      - %s\n", q(k))
		}
	}
	fmt.Fprintf(&b, "ssh_pwauth: %t\n", ci.Password != "")
	b.WriteString("package_update: true\npackages:\n  - qemu-guest-agent\n")
	for _, p := range ci.Packages {
		if p != "qemu-guest-agent" {
			fmt.Fprintf(&b, "  - %s\n", q(p))
		}
	}
	b.WriteString("runcmd:\n  - [systemctl, enable, --now, qemu-guest-agent]\n")
	return b.String()
}

func (ci *CloudInit) metaData() string {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", "virtomir-"+hex.EncodeToString(id), q(ci.Hostname))
}

// isSeedISO reports whether disk is a cloud-init seed made by VirtoMir (<machine>-cidata.iso).
func isSeedISO(disk libvirtxml.DomainDisk) bool {
	return disk.Device == "cdrom" && disk.Source != nil && disk.Source.File != nil &&
		strings.HasSuffix(disk.Source.File.File, "-cidata.iso")
}

// seedISO builds the NoCloud seed image: an ISO 9660 volume labelled "cidata" holding
// user-data and meta-data.
func (ci *CloudInit) seedISO() ([]byte, error) {
	w, err := iso9660.NewWriter()
	if err != nil {
		return nil, err
	}
	defer func() { _ = w.Cleanup() }()
	if err := w.AddFile(strings.NewReader(ci.userData()), "user-data"); err != nil {
		return nil, err
	}
	if err := w.AddFile(strings.NewReader(ci.metaData()), "meta-data"); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := w.WriteTo(&buf, "cidata"); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
