package ha

// Watchdog is the host's hardware (or softdog) watchdog: once armed, the kernel resets the
// machine unless Pet is called within the timeout. Disarm stops it cleanly.
type Watchdog interface {
	Arm(timeoutSec int) error
	Pet() error
	Disarm() error
	Armed() bool
}
