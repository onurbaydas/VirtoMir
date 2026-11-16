package virt

import (
	"context"
	"fmt"
	"sync"

	"github.com/digitalocean/go-libvirt"
)

// ConfigEvent says that a machine's configuration may have changed (configuration history,
// ADR-0021): its saved definition (Persistent), its running configuration (Live), or it
// stopped (Stopped: the next running configuration is a fresh start).
type ConfigEvent struct {
	UUID       string
	Persistent bool
	Live       bool
	Stopped    bool
	// What happened to the machine, for its timeline ("" for a change of the definition
	// only): started, stopped, paused, resumed, crashed or shutdown, and why (Detail).
	Lifecycle string
	Detail    string
}

// ConfigEvents streams the libvirt events that change machine configurations: defined and
// undefined machines, starts and stops, and devices added or removed while running
// (hot-plug, e.g. virsh attach-device --live). The channel closes when ctx ends or the
// connection to libvirt drops; callers subscribe again after reconnecting. Device events
// are optional: an old libvirt without them still delivers the lifecycle events.
//
// It subscribes on a connection of its own: libvirt 12 refuses a second callback for the
// same event on one connection ("event callback already tracked"), and the live view
// (LifecycleEvents) subscribes on the shared one.
func (c *Client) ConfigEvents(ctx context.Context) (<-chan ConfigEvent, error) {
	l, err := c.dial()
	if err != nil {
		return nil, err
	}
	subCtx, cancel := context.WithCancel(ctx)
	lifecycle, err := l.LifecycleEvents(subCtx)
	if err != nil {
		cancel()
		l.Disconnect()
		return nil, fmt.Errorf("subscribe to lifecycle events: %w", err)
	}
	var device []<-chan any
	for _, id := range []libvirt.DomainEventID{libvirt.DomainEventIDDeviceAdded, libvirt.DomainEventIDDeviceRemoved} {
		ch, err := l.SubscribeEvents(subCtx, id, libvirt.OptDomain{})
		if err != nil {
			c.log.Debug("device events unavailable", "event", id, "err", err)
			continue
		}
		device = append(device, ch)
	}

	out := make(chan ConfigEvent)
	var wg sync.WaitGroup
	send := func(ev ConfigEvent) bool {
		select {
		case out <- ev:
			return true
		case <-subCtx.Done():
			return false
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for msg := range lifecycle {
			if ev, ok := lifecycleConfigEvent(msg); ok && !send(ev) {
				break
			}
		}
		cancel()
		for range lifecycle { // go-libvirt's sender blocks until its channel closes
		}
	}()
	for _, ch := range device {
		wg.Add(1)
		go func(ch <-chan any) {
			defer wg.Done()
			for msg := range ch {
				if uuid := deviceEventUUID(msg); uuid != "" && !send(ConfigEvent{UUID: uuid, Live: true}) {
					break
				}
			}
			cancel()
			for range ch {
			}
		}(ch)
	}
	go func() {
		select {
		case <-l.Disconnected():
		case <-subCtx.Done():
		}
		cancel()
	}()
	go func() {
		wg.Wait()
		close(out)
		l.Disconnect()
	}()
	return out, nil
}

func lifecycleConfigEvent(m libvirt.DomainEventLifecycleMsg) (ConfigEvent, bool) {
	ev := ConfigEvent{UUID: formatUUID(m.Dom.UUID)}
	ev.Lifecycle, ev.Detail = lifecycleOf(m.Event, m.Detail)
	switch libvirt.DomainEventType(m.Event) {
	case libvirt.DomainEventDefined:
		ev.Persistent = true
	case libvirt.DomainEventStarted:
		ev.Live = true // the running configuration as it came up
	case libvirt.DomainEventStopped:
		ev.Stopped = true
	default:
		// Undefined (nothing left to capture), paused, resumed, …: no configuration to
		// look at, but the timeline wants to know what happened.
		return ev, ev.Lifecycle != ""
	}
	return ev, true
}

// lifecycleOf names a libvirt lifecycle event and its reason in the words of a machine's
// timeline. The reasons are libvirt's own; the ones people look for afterwards are a
// machine that stopped by itself (crashed, failed, shut down from inside) and one that was
// paused because its disk could not be written (ioerror: a full pool).
func lifecycleOf(event, detail int32) (what, why string) {
	pick := func(names ...string) string {
		if detail >= 0 && int(detail) < len(names) {
			return names[detail]
		}
		return ""
	}
	switch libvirt.DomainEventType(event) {
	case libvirt.DomainEventStarted:
		return "started", pick("booted", "migrated", "restored", "from-snapshot", "wakeup", "recreated")
	case libvirt.DomainEventSuspended:
		return "paused", pick("paused", "migrated", "ioerror", "watchdog", "restored", "from-snapshot", "api-error", "postcopy", "postcopy-failed")
	case libvirt.DomainEventResumed:
		return "resumed", pick("unpaused", "migrated", "from-snapshot", "postcopy", "postcopy-failed")
	case libvirt.DomainEventStopped:
		return "stopped", pick("shutdown", "destroyed", "crashed", "migrated", "saved", "failed", "from-snapshot", "recreated")
	case libvirt.DomainEventShutdown:
		// Who asked for it; "finished" (0) says nothing the stop that follows does not.
		if why = pick("", "guest", "host"); why != "" {
			return "shutdown", why
		}
	case libvirt.DomainEventCrashed:
		return "crashed", pick("panicked", "crashloaded")
	}
	return "", ""
}

func deviceEventUUID(msg any) string {
	switch m := msg.(type) {
	case *libvirt.DomainEventCallbackDeviceAddedMsg:
		return formatUUID(m.Dom.UUID)
	case *libvirt.DomainEventCallbackDeviceRemovedMsg:
		return formatUUID(m.Msg.Dom.UUID)
	}
	return ""
}

// DomainActive reports whether a machine is running (configuration history keeps live
// versions only of running machines).
func (c *Client) DomainActive(uuid string) (bool, error) {
	l, d, err := c.lookup(uuid)
	if err != nil {
		return false, err
	}
	on, err := l.DomainIsActive(d)
	if err != nil {
		return false, classify(err)
	}
	return on == 1, nil
}
