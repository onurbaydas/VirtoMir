//go:build linux

package ha

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// WDIOC_SETTIMEOUT from <linux/watchdog.h>: _IOWR('W', 6, int).
const wdiocSetTimeout = 0xc0045706

// devWatchdog drives /dev/watchdog. Without a hardware watchdog, `modprobe softdog` gives a
// kernel one that resets the host the same way (but not if the kernel itself hangs).
type devWatchdog struct {
	mu sync.Mutex
	f  *os.File
}

func NewWatchdog() Watchdog { return &devWatchdog{} }

func (w *devWatchdog) Arm(timeoutSec int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		return nil
	}
	f, err := os.OpenFile("/dev/watchdog", os.O_WRONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("/dev/watchdog does not exist: load a driver (modprobe softdog, or the board's iTCO_wdt/ipmi_watchdog)")
		}
		return fmt.Errorf("open /dev/watchdog (is another watchdog daemon using it?): %w", err)
	}
	if err := unix.IoctlSetPointerInt(int(f.Fd()), wdiocSetTimeout, timeoutSec); err != nil {
		// Some drivers have a fixed timeout; the device is armed anyway, so keep it.
		_, _ = f.Write([]byte{0})
	}
	w.f = f
	return nil
}

func (w *devWatchdog) Pet() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	_, err := w.f.Write([]byte{0})
	return err
}

// Disarm writes the magic 'V' before closing, which stops the timer (unless the driver was
// built with nowayout).
func (w *devWatchdog) Disarm() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	_, _ = w.f.Write([]byte("V"))
	err := w.f.Close()
	w.f = nil
	return err
}

func (w *devWatchdog) Armed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f != nil
}
