//go:build !linux

package ha

import "errors"

type noWatchdog struct{}

func NewWatchdog() Watchdog { return noWatchdog{} }

func (noWatchdog) Arm(int) error { return errors.New("hardware watchdogs are only supported on Linux") }
func (noWatchdog) Pet() error    { return nil }
func (noWatchdog) Disarm() error { return nil }
func (noWatchdog) Armed() bool   { return false }
