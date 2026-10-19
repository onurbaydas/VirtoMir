package virt

import (
	"github.com/digitalocean/go-libvirt"
)

// DRHost is the libvirt side of disaster recovery on this host (internal/dr's
// VirtController): it defines the recovered machines, powers them and removes them
// again. Disks are never touched here; the replicas and their clones belong to ZFS.
type DRHost struct{ C *Client }

// GetDomainXML returns the persistent definition of a machine.
func (h DRHost) GetDomainXML(uuid string) (string, error) { return h.C.DomainXML(uuid, true) }

// DefineDomain defines a new machine from xmlDoc and returns its UUID.
func (h DRHost) DefineDomain(xmlDoc string) (string, error) {
	l, err := h.C.conn()
	if err != nil {
		return "", err
	}
	d, err := l.DomainDefineXMLFlags(xmlDoc, libvirt.DomainDefineValidate)
	if err != nil {
		return "", classify(err)
	}
	h.C.log.Info("domain defined for disaster recovery", "domain", d.Name)
	return formatUUID(d.UUID), nil
}

// StartDomain starts a machine.
func (h DRHost) StartDomain(uuid string) error { return h.C.DomainAction(uuid, ActionStart) }

// StopDomain shuts a machine down, or pulls the plug with force.
func (h DRHost) StopDomain(uuid string, force bool) error {
	if force {
		return h.C.DomainAction(uuid, ActionForceOff)
	}
	return h.C.DomainAction(uuid, ActionShutdown)
}

// UndefineDomain removes a machine's definition (with its UEFI variables), keeping its disks.
func (h DRHost) UndefineDomain(uuid string) error {
	l, d, err := h.C.lookup(uuid)
	if err != nil {
		return err
	}
	if err := l.DomainUndefineFlags(d, libvirt.DomainUndefineNvram|libvirt.DomainUndefineManagedSave|libvirt.DomainUndefineSnapshotsMetadata|libvirt.DomainUndefineCheckpointsMetadata); err != nil {
		return classify(err)
	}
	h.C.log.Info("domain undefined after disaster recovery", "domain", d.Name)
	return nil
}

// DomainState is the machine's state as VirtoMir names it ("running", "shutoff", …).
func (h DRHost) DomainState(uuid string) (string, error) {
	l, d, err := h.C.lookup(uuid)
	if err != nil {
		return "", err
	}
	s, err := summary(l, d)
	if err != nil {
		return "", classify(err)
	}
	return string(s.State), nil
}
