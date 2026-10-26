package virt

import (
	"testing"

	"github.com/digitalocean/go-libvirt"
)

func TestConfigEventMapping(t *testing.T) {
	dom := libvirt.Domain{Name: "web", UUID: libvirt.UUID{0x2d, 0x86, 0x0e, 0x5c, 0x84, 0x4a, 0x4c, 0x8a, 0xbd, 0x06, 0x28, 0x2d, 0x61, 0xd6, 0xea, 0x52}}
	const uuid = "2d860e5c-844a-4c8a-bd06-282d61d6ea52"
	cases := []struct {
		event int32
		want  ConfigEvent
		ok    bool
	}{
		{int32(libvirt.DomainEventDefined), ConfigEvent{UUID: uuid, Persistent: true}, true},
		{int32(libvirt.DomainEventStarted), ConfigEvent{UUID: uuid, Live: true}, true},
		{int32(libvirt.DomainEventStopped), ConfigEvent{UUID: uuid, Stopped: true}, true},
		{int32(libvirt.DomainEventUndefined), ConfigEvent{}, false},
		{int32(libvirt.DomainEventSuspended), ConfigEvent{}, false},
	}
	for _, c := range cases {
		got, ok := lifecycleConfigEvent(libvirt.DomainEventLifecycleMsg{Dom: dom, Event: c.event})
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("event %d: %+v %v, want %+v %v", c.event, got, ok, c.want, c.ok)
		}
	}
	if u := deviceEventUUID(&libvirt.DomainEventCallbackDeviceAddedMsg{Dom: dom, DevAlias: "virtio-disk1"}); u != uuid {
		t.Errorf("device added: %q", u)
	}
	if u := deviceEventUUID(&libvirt.DomainEventCallbackDeviceRemovedMsg{Msg: libvirt.DomainEventDeviceRemovedMsg{Dom: dom}}); u != uuid {
		t.Errorf("device removed: %q", u)
	}
	if u := deviceEventUUID("something else"); u != "" {
		t.Errorf("unknown message: %q", u)
	}
}
