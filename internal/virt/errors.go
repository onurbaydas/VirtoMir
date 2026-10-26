package virt

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/digitalocean/go-libvirt"
)

// ErrNotFound means the requested object (domain, pool, volume, network, snapshot)
// does not exist (any more).
var ErrNotFound = errors.New("not found")

// OperationError is libvirt (or VirtoMir) refusing an operation on a reachable host,
// e.g. starting a domain that is already running. The message is meant for the user.
type OperationError struct{ Message string }

func (e *OperationError) Error() string { return e.Message }

// InvalidError means the request itself is wrong: malformed XML, a bad name, a size of 0.
// The message tells the user what to fix.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

func invalidf(format string, a ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, a...)}
}

func refusedf(format string, a ...any) error {
	return &OperationError{Message: fmt.Sprintf(format, a...)}
}

// dropVolume deletes a volume an operation created before it failed (rollback). If that
// fails too, the leftover is logged so an admin can find and remove it.
func dropVolume(log *slog.Logger, l *libvirt.Libvirt, v libvirt.StorageVol) {
	if err := l.StorageVolDelete(v, libvirt.StorageVolDeleteNormal); err != nil {
		log.Warn("rollback: could not delete a volume left by a failed operation; remove it by hand",
			"pool", v.Pool, "volume", v.Name, "path", v.Key, "err", err)
	}
}

// classify turns libvirt RPC errors into ErrNotFound / *InvalidError / *OperationError;
// other errors (connection problems) pass through unchanged.
func classify(err error) error {
	var lerr libvirt.Error
	if !errors.As(err, &lerr) {
		return err
	}
	switch libvirt.ErrorNumber(lerr.Code) {
	case libvirt.ErrNoDomain, libvirt.ErrNoNetwork, libvirt.ErrNoStoragePool, libvirt.ErrNoStorageVol, libvirt.ErrNoDomainSnapshot:
		return fmt.Errorf("%w: %s", ErrNotFound, lerr.Message)
	case libvirt.ErrXMLError, libvirt.ErrXMLDetail, libvirt.ErrInvalidArg, libvirt.ErrConfigUnsupported:
		return &InvalidError{Message: lerr.Message}
	}
	return &OperationError{Message: lerr.Message}
}

// Names of machines, volumes, pools and networks end up in file names and command lines;
// VirtoMir keeps them to a safe, portable set.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

func checkName(what, name string) error {
	if !nameRE.MatchString(name) {
		return invalidf("%s name %q is not allowed: use 1–64 letters, digits, '.', '_', '+' or '-', starting with a letter or digit", what, name)
	}
	return nil
}

// CheckName validates machine, volume, pool, or network names.
func CheckName(what, name string) error {
	return checkName(what, name)
}
