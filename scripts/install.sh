#!/bin/sh
# VirtoMir installer: puts virtomird, its PAM service and a systemd unit on this host.
#
#   sudo ./install.sh                         # from an unpacked release (this directory)
#   sudo ./install.sh --from virtomir_v0.1.0_linux_amd64.tar.gz
#   sudo ./install.sh --download              # latest GitHub release (gh CLI for private repos)
#   sudo ./install.sh --listen 127.0.0.1:8420 # only local access (behind a reverse proxy)
#   sudo ./install.sh --uninstall [--purge]   # remove (--purge also deletes /var/lib/virtomir)
#
# Re-running it upgrades in place and keeps settings, certificate and metric history.
set -eu

REPO=onurbaydas/VirtoMir
BIN=/usr/local/bin/virtomird
UNIT=/etc/systemd/system/virtomird.service
PAMFILE=/etc/pam.d/virtomir
STATE=/var/lib/virtomir
LISTEN=0.0.0.0:8420
FROM=""
DOWNLOAD=0
UNINSTALL=0
PURGE=0
SKIP_CHECKS=0

say() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
	--from) FROM=${2:?--from needs a file or directory}; shift ;;
	--download) DOWNLOAD=1 ;;
	--listen) LISTEN=${2:?--listen needs an address}; shift ;;
	--uninstall) UNINSTALL=1 ;;
	--purge) PURGE=1 ;;
	--skip-checks) SKIP_CHECKS=1 ;;
	-h | --help) sed -n '2,12p' "$0"; exit 0 ;;
	*) die "unknown option $1 (see --help)" ;;
	esac
	shift
done

[ "$(id -u)" = 0 ] || die "run as root (sudo $0 $*)"
[ "$(uname -s)" = Linux ] || die "VirtoMir runs on Linux hosts"
command -v systemctl >/dev/null || die "systemd is required"

if [ "$UNINSTALL" = 1 ]; then
	say "Removing VirtoMir"
	systemctl disable --now virtomird.service 2>/dev/null || true
	rm -f "$UNIT" "$BIN" "$PAMFILE"
	systemctl daemon-reload
	if [ "$PURGE" = 1 ]; then
		rm -rf "$STATE"
		say "Removed $STATE (certificate, metric history)"
	else
		say "Kept $STATE (certificate, metric history); --purge removes it"
	fi
	say "Done. Your virtual machines and libvirt were not touched."
	exit 0
fi

# ---- Checks ---------------------------------------------------------------------------
if [ "$SKIP_CHECKS" = 0 ]; then
	if [ ! -S /var/run/libvirt/libvirt-sock ] && [ ! -S /run/libvirt/libvirt-sock ]; then
		warn "libvirt does not seem to be running (no /run/libvirt/libvirt-sock)."
		if command -v apt-get >/dev/null; then
			warn "Install it with: apt-get install -y qemu-system-x86 libvirt-daemon-system ovmf swtpm"
		elif command -v dnf >/dev/null; then
			warn "Install it with: dnf install -y @virtualization edk2-ovmf swtpm && systemctl enable --now libvirtd"
		fi
		die "start libvirt first, or pass --skip-checks"
	fi
	[ -e /dev/kvm ] || warn "/dev/kvm is missing: virtual machines will run without hardware acceleration (slow)."
fi

ARCH=$(uname -m)
case "$ARCH" in
x86_64) ARCH=amd64 ;;
aarch64) ARCH=arm64 ;;
esac

# ---- Get the release files ------------------------------------------------------------
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

if [ "$DOWNLOAD" = 1 ]; then
	say "Downloading the latest release for linux/$ARCH"
	if command -v gh >/dev/null && gh auth status >/dev/null 2>&1; then
		gh release download -R "$REPO" -p "*_linux_${ARCH}.tar.gz*" -D "$WORK" ||
			die "download failed (is there a release for linux/$ARCH?)"
	else
		command -v curl >/dev/null || die "curl or gh is required for --download"
		URL=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
			grep -o "https://[^\"]*_linux_${ARCH}\.tar\.gz" | head -n1) ||
			die "no public release found; for a private repository install and sign in to the gh CLI, or use --from"
		curl -fsSL -o "$WORK/release.tar.gz" "$URL"
		curl -fsSL -o "$WORK/release.tar.gz.sha256" "$URL.sha256" || true
	fi
	for f in "$WORK"/*.tar.gz; do
		FROM=$f
		break
	done
fi

if [ -z "$FROM" ]; then
	FROM=$(cd "$(dirname "$0")" && pwd) # unpacked release next to this script
fi

if [ -f "$FROM" ]; then
	if [ -f "$FROM.sha256" ]; then
		say "Verifying checksum"
		expected=$(cut -d' ' -f1 <"$FROM.sha256")
		actual=$(sha256sum "$FROM" | cut -d' ' -f1)
		[ "$expected" = "$actual" ] || die "checksum mismatch for $FROM"
	fi
	tar -xzf "$FROM" -C "$WORK"
	SRC=$(find "$WORK" -maxdepth 2 -name virtomird -type f -exec dirname {} \; | head -n1)
else
	SRC=$FROM
fi
if [ -z "${SRC:-}" ] || [ ! -x "$SRC/virtomird" ]; then
	die "no virtomird binary found in $FROM"
fi

# ---- Install --------------------------------------------------------------------------
UPGRADE=0
[ -f "$BIN" ] && UPGRADE=1

say "Installing $BIN"
install -m 0755 "$SRC/virtomird" "$BIN.new"
mv -f "$BIN.new" "$BIN"

say "Installing PAM service $PAMFILE"
if [ -f /etc/pam.d/common-auth ]; then
	install -m 0644 "$SRC/virtomir.pam" "$PAMFILE" # Debian / Ubuntu
else
	cat >"$PAMFILE" <<'EOF'
#%PAM-1.0
# PAM service for VirtoMir sign-in (Fedora / RHEL / Arch style).
auth     include system-auth
account  include system-auth
EOF
	chmod 0644 "$PAMFILE"
fi

say "Installing systemd unit (listening on $LISTEN)"
sed "s|-listen 0.0.0.0:8420|-listen $LISTEN|" "$SRC/virtomird.service" >"$UNIT"
chmod 0644 "$UNIT"
install -d -m 0750 "$STATE"
systemctl daemon-reload
if [ "$UPGRADE" = 1 ]; then
	systemctl restart virtomird.service
else
	systemctl enable --now virtomird.service
fi

sleep 1
systemctl is-active --quiet virtomird.service || {
	journalctl -u virtomird.service -n 20 --no-pager >&2 || true
	die "virtomird did not start (log above)"
}

# ---- Tell the user where to go --------------------------------------------------------
HOST=$(hostname -I 2>/dev/null | awk '{print $1}')
PORT=${LISTEN##*:}
case "$LISTEN" in 127.* | localhost:*) HOST=127.0.0.1 ;; esac
echo
say "VirtoMir is running: https://${HOST:-$(hostname)}:$PORT/"
echo "    Sign in with root or a member of the 'libvirt' group (usermod -aG libvirt <user>)."
echo "    The certificate is self-signed: your browser will ask you to confirm it once."
echo "    Replace it with -tls files -tls-cert … -tls-key … in $UNIT if you have a real one."
if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
	echo "    Firewall: ufw allow $PORT/tcp"
elif command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
	echo "    Firewall: firewall-cmd --permanent --add-port=$PORT/tcp && firewall-cmd --reload"
fi
echo "    Logs: journalctl -u virtomird -f    Uninstall: sudo $0 --uninstall"
