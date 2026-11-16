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
		{int32(libvirt.DomainEventStarted), ConfigEvent{UUID: uuid, Live: true, Lifecycle: "started", Detail: "booted"}, true},
		{int32(libvirt.DomainEventStopped), ConfigEvent{UUID: uuid, Stopped: true, Lifecycle: "stopped", Detail: "shutdown"}, true},
		{int32(libvirt.DomainEventUndefined), ConfigEvent{}, false},
		// No configuration to look at, but the timeline is told.
		{int32(libvirt.DomainEventSuspended), ConfigEvent{UUID: uuid, Lifecycle: "paused", Detail: "paused"}, true},
		{int32(libvirt.DomainEventCrashed), ConfigEvent{UUID: uuid, Lifecycle: "crashed", Detail: "panicked"}, true},
		{int32(libvirt.DomainEventShutdown), ConfigEvent{}, false}, // "finished": the stop that follows says it
	}
	for _, c := range cases {
		got, ok := lifecycleConfigEvent(libvirt.DomainEventLifecycleMsg{Dom: dom, Event: c.event})
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("event %d: %+v %v, want %+v %v", c.event, got, ok, c.want, c.ok)
		}
	}
	for _, c := range []struct {
		event, detail int32
		what, why     string
	}{
		{int32(libvirt.DomainEventSuspended), int32(libvirt.DomainEventSuspendedIoerror), "paused", "ioerror"},
		{int32(libvirt.DomainEventStopped), int32(libvirt.DomainEventStoppedCrashed), "stopped", "crashed"},
		{int32(libvirt.DomainEventStopped), int32(libvirt.DomainEventStoppedFailed), "stopped", "failed"},
		{int32(libvirt.DomainEventStopped), int32(libvirt.DomainEventStoppedDestroyed), "stopped", "destroyed"},
		{int32(libvirt.DomainEventShutdown), int32(libvirt.DomainEventShutdownGuest), "shutdown", "guest"},
		{int32(libvirt.DomainEventStarted), int32(libvirt.DomainEventStartedFromSnapshot), "started", "from-snapshot"},
		{int32(libvirt.DomainEventResumed), 0, "resumed", "unpaused"},
		{int32(libvirt.DomainEventStopped), 99, "stopped", ""}, // a reason this libvirt binding does not know
		{int32(libvirt.DomainEventStopped), -1, "stopped", ""},
		{int32(libvirt.DomainEventDefined), 0, "", ""},
		{1234, 0, "", ""},
	} {
		if what, why := lifecycleOf(c.event, c.detail); what != c.what || why != c.why {
			t.Errorf("lifecycleOf(%d, %d) = %q %q, want %q %q", c.event, c.detail, what, why, c.what, c.why)
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
