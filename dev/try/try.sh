#!/bin/sh
# One command to a throwaway VirtoMir test host in a Multipass VM (Linux, macOS on Intel,
# Windows). Nothing is installed on your own machine besides the VM; delete it when done.
#
#   sh dev/try/try.sh                 create the VM "virtomir-try" and install VirtoMir in it
#   sh dev/try/try.sh --cpus 6 --memory 12G --disk 60G
#   sh dev/try/try.sh --delete        remove the VM and everything in it
#
# Needs Multipass (https://canonical.com/multipass). Machines inside the VM need nested
# virtualization; see docs/try-in-a-vm.md if the script warns about /dev/kvm.
set -eu

NAME=virtomir-try
CPUS=4
MEMORY=6G
DISK=40G
IMAGE=24.04
HERE=$(cd "$(dirname "$0")" && pwd)

say() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
	--name) NAME=${2:?--name needs a value}; shift ;;
	--cpus) CPUS=${2:?--cpus needs a value}; shift ;;
	--memory) MEMORY=${2:?--memory needs a value, e.g. 8G}; shift ;;
	--disk) DISK=${2:?--disk needs a value, e.g. 60G}; shift ;;
	--delete)
		multipass delete --purge "$NAME" && say "Deleted $NAME"
		exit 0
		;;
	-h | --help) sed -n '2,11p' "$0"; exit 0 ;;
	*) die "unknown option $1 (see --help)" ;;
	esac
	shift
done

command -v multipass >/dev/null || die "Multipass is not installed: https://canonical.com/multipass/install"
[ -r "$HERE/provision.sh" ] || die "provision.sh not found next to this script"

if multipass info "$NAME" >/dev/null 2>&1; then
	say "Reusing the existing VM $NAME (sh $0 --delete to start over)"
	multipass start "$NAME" >/dev/null 2>&1 || true
else
	say "Creating $NAME: Ubuntu $IMAGE, $CPUS vCPUs, $MEMORY memory, $DISK disk"
	multipass launch "$IMAGE" --name "$NAME" --cpus "$CPUS" --memory "$MEMORY" --disk "$DISK"
fi

if ! multipass exec "$NAME" -- test -e /dev/kvm; then
	warn "the VM has no /dev/kvm (nested virtualization is off on this computer)."
	warn "VirtoMir's UI will work, but machines you create inside will not start."
	warn "How to turn it on: docs/try-in-a-vm.md#nested-virtualization"
fi

multipass transfer "$HERE/provision.sh" "$NAME:/tmp/virtomir-provision.sh"
multipass exec "$NAME" -- sudo sh /tmp/virtomir-provision.sh

say "Done. Shell into the VM with: multipass shell $NAME"
