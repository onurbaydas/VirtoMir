#!/bin/sh
# Turns a fresh, throwaway Debian 13 / Ubuntu 24.04 or Fedora / RHEL 10 (Rocky, Alma) VM into a
# VirtoMir test host: KVM/libvirt, VirtoMir from the signed apt or dnf repository (or the latest
# GitHub release), a sign-in user with a random password, a storage pool and network, and a
# tiny test image.
#
#   sudo sh provision.sh
#
# Used by dev/try/try.sh (Multipass) and dev/try/Vagrantfile; works the same on any VM with
# nested virtualization (a cloud VM, Proxmox, Hyper-V…). Meant for a VM you can delete:
# it installs packages and creates a user. Read docs/try-in-a-vm.md first.
#
# Environment:
#   TRY_USER      sign-in user to create (default: virtomir)
#   TRY_IMAGE     1 (default) downloads a small CirrOS test image into the default pool; 0 skips it
#   VIRTOMIR_DEB  install this .deb instead of downloading one (path inside the VM)
#   VIRTOMIR_RPM  the same for an .rpm on Fedora / RHEL
set -eu

REPO=onurbaydas/VirtoMir
REPO_URL=https://onurbaydas.github.io/VirtoMir
TRY_USER=${TRY_USER:-virtomir}
TRY_IMAGE=${TRY_IMAGE:-1}
CIRROS_DIR=https://download.cirros-cloud.net/0.6.3
CIRROS_FILE=cirros-0.6.3-x86_64-disk.img
PASSFILE=/root/virtomir-try-password

say() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "run as root: sudo sh $0"
[ -r /etc/os-release ] || die "no /etc/os-release: use Debian 13, Ubuntu 24.04, Fedora or RHEL 10"
# shellcheck disable=SC1091
. /etc/os-release
# FAMILY: deb (apt) or rpm (dnf); the rest of the script is the same for both.
case " ${ID:-} ${ID_LIKE:-} " in
*" debian "* | *" ubuntu "*) FAMILY=deb ;;
*" fedora "* | *" rhel "* | *" centos "*) FAMILY=rpm ;;
*) die "this script knows Debian, Ubuntu, Fedora and RHEL-like systems (found ${PRETTY_NAME:-unknown})" ;;
esac
if [ "$FAMILY" = deb ]; then
	ARCH=$(dpkg --print-architecture)
	export DEBIAN_FRONTEND=noninteractive
else
	ARCH=$(uname -m) # x86_64, aarch64: the .rpm names
	command -v dnf >/dev/null || die "dnf is missing"
fi

# ---- 1. KVM -------------------------------------------------------------------------------
if [ -e /dev/kvm ]; then
	say "KVM is available (nested virtualization works)"
else
	warn "/dev/kvm is missing: this VM has no nested virtualization."
	warn "VirtoMir installs and its UI works, but machines inside will not start (or crawl)."
	warn "Turn on nested virtualization for this VM (see docs/try-in-a-vm.md) and run this again."
fi

# ---- 2. Packages --------------------------------------------------------------------------
say "Installing KVM, QEMU and libvirt"
if [ "$FAMILY" = deb ]; then
	apt-get update -qq
	apt-get install -y -qq --no-install-recommends \
		ca-certificates curl gnupg openssl \
		qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients ovmf swtpm swtpm-tools dnsmasq-base \
		>/dev/null
else
	# The virtualization group of Fedora and RHEL: QEMU/KVM, the modular libvirt daemons, virsh.
	dnf install -y -q ca-certificates curl openssl iproute qemu-kvm qemu-img libvirt libvirt-client \
		edk2-ovmf swtpm swtpm-tools dnsmasq >/dev/null
fi

# ---- 3. VirtoMir --------------------------------------------------------------------------
# The .rpm's own scripts start libvirt's sockets, open port 8420 in firewalld and load the
# SELinux module where SELinux is on.
if [ "$FAMILY" = rpm ]; then
	if [ -n "${VIRTOMIR_RPM:-}" ]; then
		say "Installing $VIRTOMIR_RPM"
		dnf install -y -q "$VIRTOMIR_RPM" >/dev/null
	elif curl -fsSL -o /etc/yum.repos.d/virtomir.repo "$REPO_URL/virtomir.repo" 2>/dev/null; then
		say "Installing VirtoMir from the signed dnf repository"
		dnf install -y -q virtomir >/dev/null
	else
		say "No dnf repository reachable; installing the latest release's .rpm from GitHub"
		urls=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep -o "https://[^\"]*\.${ARCH}\.rpm[^\"]*") ||
			die "no release with an .rpm for $ARCH found"
		rpm=$(echo "$urls" | grep '\.rpm$' | head -n1)
		sum=$(echo "$urls" | grep '\.rpm\.sha256$' | head -n1)
		work=$(mktemp -d)
		curl -fsSL -o "$work/$(basename "$rpm")" "$rpm"
		if [ -n "$sum" ]; then
			curl -fsSL -o "$work/sum" "$sum"
			(cd "$work" && sha256sum -c sum >/dev/null) || die "checksum of $(basename "$rpm") does not match the release"
			say "Checksum verified"
		else
			warn "the release has no .sha256 for $(basename "$rpm"); installing unverified"
		fi
		dnf install -y -q "$work/$(basename "$rpm")" >/dev/null
		rm -rf "$work"
	fi
elif [ -n "${VIRTOMIR_DEB:-}" ]; then
	say "Installing $VIRTOMIR_DEB"
	apt-get install -y -qq "$VIRTOMIR_DEB" >/dev/null
elif curl -fsSL -o /tmp/virtomir.asc "$REPO_URL/virtomir.asc" 2>/dev/null; then
	say "Installing VirtoMir from the signed apt repository"
	install -d -m 0755 /etc/apt/keyrings
	install -m 0644 /tmp/virtomir.asc /etc/apt/keyrings/virtomir.asc
	echo "deb [signed-by=/etc/apt/keyrings/virtomir.asc] $REPO_URL/apt stable main" >/etc/apt/sources.list.d/virtomir.list
	apt-get update -qq
	apt-get install -y -qq virtomir >/dev/null
else
	say "No apt repository reachable; installing the latest release's .deb from GitHub"
	urls=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep -o "https://[^\"]*_${ARCH}\.deb[^\"]*") ||
		die "no release with a .deb for $ARCH found"
	deb=$(echo "$urls" | grep '\.deb$' | head -n1)
	sum=$(echo "$urls" | grep '\.deb\.sha256$' | head -n1)
	work=$(mktemp -d)
	curl -fsSL -o "$work/$(basename "$deb")" "$deb"
	if [ -n "$sum" ]; then
		curl -fsSL -o "$work/sum" "$sum"
		(cd "$work" && sha256sum -c sum >/dev/null) || die "checksum of $(basename "$deb") does not match the release"
		say "Checksum verified"
	else
		warn "the release has no .sha256 for $(basename "$deb"); installing unverified"
	fi
	apt-get install -y -qq "$work/$(basename "$deb")" >/dev/null
	rm -rf "$work"
fi
systemctl enable --now virtomird.service >/dev/null 2>&1 || true

# ---- 4. A user to sign in with ------------------------------------------------------------
if ! id "$TRY_USER" >/dev/null 2>&1; then
	say "Creating the sign-in user $TRY_USER"
	useradd --create-home --shell /bin/bash "$TRY_USER"
fi
usermod -aG libvirt "$TRY_USER"
if [ ! -s "$PASSFILE" ]; then
	openssl rand -base64 18 | tr -d '/+=' | cut -c1-16 >"$PASSFILE"
	chmod 600 "$PASSFILE"
	echo "$TRY_USER:$(cat "$PASSFILE")" | chpasswd
fi

# ---- 5. Somewhere to put machines ---------------------------------------------------------
say "Default network and storage pool"
# libvirt may still be starting right after its install.
n=0
until virsh -q version >/dev/null 2>&1; do
	n=$((n + 1))
	[ "$n" -lt 30 ] || break
	sleep 1
done
if virsh -q version >/dev/null 2>&1; then
	virsh -q net-autostart default >/dev/null 2>&1 || true
	virsh -q net-start default >/dev/null 2>&1 || true
	if ! virsh -q pool-info default >/dev/null 2>&1; then
		install -d -m 0711 /var/lib/libvirt/images
		if ! { virsh -q pool-define-as default dir --target /var/lib/libvirt/images >/dev/null &&
			virsh -q pool-autostart default >/dev/null && virsh -q pool-start default >/dev/null; }; then
			warn "could not create the storage pool; VirtoMir's first-run page offers to"
		fi
	fi
else
	warn "libvirt does not answer (systemctl status libvirtd); VirtoMir's first-run page will help once it does"
fi

if [ "$TRY_IMAGE" = 1 ] && [ ! -e /var/lib/libvirt/images/cirros-0.6.3.qcow2 ]; then
	say "Downloading a tiny test image (CirrOS, ~20 MB)"
	img=$(mktemp -d)
	# Checked against the SHA256SUMS that CirrOS publishes next to the image.
	if curl -fsSL -o "$img/$CIRROS_FILE" "$CIRROS_DIR/$CIRROS_FILE" &&
		curl -fsSL -o "$img/SHA256SUMS" "$CIRROS_DIR/SHA256SUMS" &&
		(cd "$img" && grep "[ *]$CIRROS_FILE\$" SHA256SUMS | sha256sum -c - >/dev/null 2>&1); then
		qemu-img convert -f qcow2 -O qcow2 "$img/$CIRROS_FILE" /var/lib/libvirt/images/cirros-0.6.3.qcow2
		virsh -q pool-refresh default >/dev/null 2>&1 || true
	else
		warn "could not download or verify the CirrOS image; upload any image from the Storage page instead"
	fi
	rm -rf "$img"
fi

# ---- Done ---------------------------------------------------------------------------------
ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
[ -n "$ip" ] || ip="<the address of this VM>"
port=8420
status="VirtoMir is running."
if ! curl -fsk -o /dev/null "https://127.0.0.1:$port/healthz"; then
	sleep 3
	if ! curl -fsk -o /dev/null "https://127.0.0.1:$port/healthz"; then
		warn "virtomird does not answer yet: journalctl -u virtomird"
		status="VirtoMir is installed, but does not answer yet (journalctl -u virtomird)."
	fi
fi
cat <<EOF

  $status

    Open      https://$ip:$port/   (accept the self-signed certificate once)
    User      $TRY_USER
    Password  $(cat "$PASSFILE")   (also in $PASSFILE)

  Things to try: create a machine from the CirrOS image (Machines → New machine →
  Existing disk), open its console, take a snapshot, watch the charts.
  Tell us how it went: https://github.com/$REPO/issues/new/choose

EOF
