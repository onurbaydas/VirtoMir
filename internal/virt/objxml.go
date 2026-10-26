package virt

import (
	"slices"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// Raw XML of virtual networks and storage pools (P4: every setting reachable). Saving
// redefines the persistent object; a running network or pool keeps its old settings until
// it is restarted, which ObjectSaveResult says.

// ObjectSaveResult reports how a saved definition takes effect.
type ObjectSaveResult struct {
	// RestartNeeded: the object is running and only uses the new definition after a restart.
	RestartNeeded bool `json:"restartNeeded"`
}

// NetworkXML returns a network's persistent (inactive) definition.
func (c *Client) NetworkXML(name string) (string, error) {
	l, err := c.conn()
	if err != nil {
		return "", err
	}
	n, err := l.NetworkLookupByName(name)
	if err != nil {
		return "", classify(err)
	}
	doc, err := l.NetworkGetXMLDesc(n, 1) // VIR_NETWORK_XML_INACTIVE
	return doc, classify(err)
}

// DefineNetworkXML replaces a network's definition. Name and UUID must stay.
func (c *Client) DefineNetworkXML(name, doc string) (ObjectSaveResult, error) {
	var res ObjectSaveResult
	var x libvirtxml.Network
	if err := x.Unmarshal(doc); err != nil {
		return res, invalidf("The XML is not valid: %v", err)
	}
	l, err := c.conn()
	if err != nil {
		return res, err
	}
	n, err := l.NetworkLookupByName(name)
	if err != nil {
		return res, classify(err)
	}
	if x.Name != name {
		return res, invalidf("The XML must keep <name>%s</name>; create a new network for another name.", name)
	}
	if x.UUID != "" && !strings.EqualFold(x.UUID, formatUUID(n.UUID)) {
		return res, invalidf("The XML must keep <uuid>%s</uuid>.", formatUUID(n.UUID))
	}
	if cur, err := l.NetworkGetXMLDesc(n, 1); err == nil && sameXML(cur, doc) {
		return res, nil // nothing changed: nothing to define, nothing to restart
	}
	if _, err := l.NetworkDefineXML(doc); err != nil {
		return res, classify(err)
	}
	if a, err := l.NetworkIsActive(n); err == nil && a == 1 {
		res.RestartNeeded = true
	}
	c.log.Info("network redefined", "network", name)
	return res, nil
}

// PoolXML returns a storage pool's persistent definition.
func (c *Client) PoolXML(name string) (string, error) {
	l, err := c.conn()
	if err != nil {
		return "", err
	}
	p, err := l.StoragePoolLookupByName(name)
	if err != nil {
		return "", classify(err)
	}
	doc, err := l.StoragePoolGetXMLDesc(p, 1) // VIR_STORAGE_XML_INACTIVE
	return doc, classify(err)
}

// DefinePoolXML replaces a storage pool's definition. Name and UUID must stay.
func (c *Client) DefinePoolXML(name, doc string) (ObjectSaveResult, error) {
	var res ObjectSaveResult
	var x libvirtxml.StoragePool
	if err := x.Unmarshal(doc); err != nil {
		return res, invalidf("The XML is not valid: %v", err)
	}
	l, err := c.conn()
	if err != nil {
		return res, err
	}
	p, err := l.StoragePoolLookupByName(name)
	if err != nil {
		return res, classify(err)
	}
	if x.Name != name {
		return res, invalidf("The XML must keep <name>%s</name>; create a new pool for another name.", name)
	}
	if x.UUID != "" && !strings.EqualFold(x.UUID, formatUUID(p.UUID)) {
		return res, invalidf("The XML must keep <uuid>%s</uuid>.", formatUUID(p.UUID))
	}
	cur, err := l.StoragePoolGetXMLDesc(p, 1)
	if err == nil && sameXML(cur, doc) {
		return res, nil
	}
	// A directory pool moved to a new path gets the same checks as a new pool.
	if (x.Type == "dir" || x.Type == "fs" || x.Type == "netfs") && x.Target != nil {
		var old libvirtxml.StoragePool
		if err != nil || old.Unmarshal(cur) != nil || old.Target == nil || old.Target.Path != x.Target.Path {
			var protected []string
			if c.ProtectedDirs != nil {
				protected = c.ProtectedDirs()
			}
			if _, err := checkPoolPath(x.Target.Path, protected); err != nil {
				return res, err
			}
		}
	}
	if _, err := l.StoragePoolDefineXML(doc, 0); err != nil {
		return res, classify(err)
	}
	if a, err := l.StoragePoolIsActive(p); err == nil && a == 1 {
		res.RestartNeeded = true
	}
	c.log.Info("storage pool redefined", "pool", name)
	return res, nil
}

// sameXML compares two definitions line by line, ignoring indentation and blank lines.
func sameXML(a, b string) bool {
	norm := func(s string) []string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	return slices.Equal(norm(a), norm(b))
}
