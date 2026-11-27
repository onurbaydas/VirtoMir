package sdn

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
)

// ipsecFile is an absolute path with no characters ovs-vsctl would read as syntax
// (quotes, commas, spaces, '=') and no '..'.
var ipsecFile = regexp.MustCompile(`^/[A-Za-z0-9._+@/-]+$`)

// IPsec for the overlay tunnels (#33). Experimental: not tested on hardware. OVN encrypts
// the Geneve tunnels between chassis when NB_Global ipsec=true and every chassis has a
// certificate, its key and the CA (Open_vSwitch other_config); the openvswitch-ipsec
// service (ovs-monitor-ipsec) then keeps the IPsec policies. OVN has no pre-shared keys.

// ValidateIPsec checks a configuration before it is stored.
func ValidateIPsec(c IPsecConfig) (IPsecConfig, error) {
	if !c.Enabled {
		c.Status = "disabled"
		return c, nil
	}
	if c.Mode == "" {
		c.Mode = "cert"
	}
	if c.Mode == "psk" {
		return c, invalidf("OVN encrypts its tunnels with certificates only; give each host's certificate, key and the CA")
	}
	if c.Mode != "cert" {
		return c, invalidf("the mode is cert")
	}
	for _, f := range []struct {
		name string
		path *string
	}{{"certificate", &c.CertFile}, {"key", &c.KeyFile}, {"CA certificate", &c.CACertFile}} {
		if !ipsecFile.MatchString(*f.path) || filepath.Clean(*f.path) != *f.path {
			return c, invalidf("the %s is an absolute path on every host, of letters, digits and . _ + @ - /", f.name)
		}
	}
	c.PSK = ""
	return c, nil
}

// ReconcileIPsec switches tunnel encryption on this chassis (and, central, in OVN).
func ReconcileIPsec(ctx context.Context, r Runner, nb NB, central bool, c IPsecConfig) error {
	// The program is always a constant; only arguments come from the configuration.
	type step struct {
		cmd  string
		args []string
	}
	var steps []step
	if c.Enabled {
		steps = append(steps,
			step{"ovs-vsctl", []string{"set", "Open_vSwitch", ".", "other_config:certificate=" + c.CertFile,
				"other_config:private_key=" + c.KeyFile, "other_config:ca_cert=" + c.CACertFile}},
			step{"systemctl", []string{"enable", "--now", "openvswitch-ipsec"}})
	} else {
		steps = append(steps,
			step{"systemctl", []string{"disable", "--now", "openvswitch-ipsec"}},
			step{"ovs-vsctl", []string{"remove", "Open_vSwitch", ".", "other_config", "certificate", "private_key", "ca_cert"}})
	}
	for _, s := range steps {
		if _, err := r.Run(ctx, s.cmd, s.args...); err != nil {
			return fmt.Errorf("ipsec: %w", err)
		}
	}
	if central {
		if _, err := nb.Run(ctx, "set", "NB_Global", ".", fmt.Sprintf("ipsec=%t", c.Enabled)); err != nil {
			return fmt.Errorf("ipsec: %w", err)
		}
	}
	return nil
}

// ---- Store -----------------------------------------------------------------------------------

// GetIPsecConfig returns the tunnel encryption settings.
func (s *Store) GetIPsecConfig() IPsecConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st.IPsec
}

// SetIPsecConfig stores the tunnel encryption settings.
func (s *Store) SetIPsecConfig(c IPsecConfig) (IPsecConfig, error) {
	c, err := ValidateIPsec(c)
	if err != nil {
		return c, err
	}
	err = s.change(func(st *state) error {
		st.IPsec = c
		return nil
	})
	return c, err
}

// SetIPsecStatus records how the last reconcile went.
func (s *Store) SetIPsecStatus(status, lastError string) error {
	return s.change(func(st *state) error {
		st.IPsec.Status, st.IPsec.LastError = status, lastError
		return nil
	})
}
