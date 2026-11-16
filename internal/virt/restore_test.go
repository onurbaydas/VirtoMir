package virt

import (
	"slices"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

const oldDefinition = `<domain type='kvm'>
  <name>web-old</name>
  <uuid>2d860e5c-844a-4c8a-bd06-282d61d6ea52</uuid>
  <metadata>
    <app:note xmlns:app="http://example.org/app"><name>not the machine</name></app:note>
  </metadata>
  <memory unit='KiB'>1048576</memory>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/var/lib/libvirt/images/web.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <source file='/var/lib/libvirt/images/debian.iso'/>
      <target dev='sda' bus='sata'/>
    </disk>
    <disk type='block' device='disk'>
      <source dev='/dev/vg0/data'/>
      <target dev='vdb' bus='virtio'/>
    </disk>
    <disk type='network' device='disk'>
      <source protocol='rbd' name='pool/image'/>
      <target dev='vdc' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <target dev='sdb' bus='sata'/>
    </disk>
  </devices>
</domain>`

// A version from before a rename is restored under the name the machine has now, and
// nothing but the name changes.
func TestRestoredDefinitionKeepsThePresentName(t *testing.T) {
	got, err := restoredDefinition(oldDefinition, "web")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(oldDefinition, "<name>web-old</name>", "<name>web</name>", 1); got != want {
		t.Errorf("only the machine's name may change:\n%s", got)
	}
	if !strings.Contains(got, "<name>not the machine</name>") {
		t.Error("a <name> inside the metadata was rewritten")
	}
	same, err := restoredDefinition(oldDefinition, "web-old")
	if err != nil || same != oldDefinition {
		t.Errorf("the same name: changed=%v err=%v", same != oldDefinition, err)
	}
	// A name libvirt accepts but XML has to escape.
	esc, err := restoredDefinition(oldDefinition, "r&d <lab>")
	var x libvirtxml.Domain
	if err != nil || x.Unmarshal(esc) != nil || x.Name != "r&d <lab>" {
		t.Errorf("escaped name: %q, %v", x.Name, err)
	}
}

func TestRestoredDefinitionRefusesWhatItCannotRename(t *testing.T) {
	for name, doc := range map[string]string{
		"not XML": "<domain><name>web",
		"no name": "<domain type='kvm'><uuid>2d860e5c-844a-4c8a-bd06-282d61d6ea52</uuid></domain>",
		// The first <name> is not the machine's: rewriting it would not rename the machine.
		"name not first": strings.Replace(strings.Replace(oldDefinition, "  <name>web-old</name>\n", "", 1), "</metadata>", "</metadata>\n  <name>web-old</name>", 1),
	} {
		if out, err := restoredDefinition(doc, "web"); err == nil {
			t.Errorf("%s: restored as\n%s", name, out)
		}
	}
}

// Disks that are gone are named; network disks and empty drives are not looked at.
func TestMissingDisks(t *testing.T) {
	var x libvirtxml.Domain
	if err := x.Unmarshal(oldDefinition); err != nil {
		t.Fatal(err)
	}
	there := map[string]bool{"/var/lib/libvirt/images/web.qcow2": true}
	var asked []string
	got := missingDisks(&x, func(p string) bool { asked = append(asked, p); return there[p] })
	if want := []string{"/var/lib/libvirt/images/debian.iso", "/dev/vg0/data"}; !slices.Equal(got, want) {
		t.Errorf("missing = %v, want %v", got, want)
	}
	if len(asked) != 3 {
		t.Errorf("looked at %v", asked)
	}
	if got := missingDisks(&libvirtxml.Domain{}, func(string) bool { return false }); len(got) != 0 {
		t.Errorf("a machine without devices: %v", got)
	}
}
