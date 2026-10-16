#!/bin/sh
# Builds a Debian/Ubuntu package on Linux: web UI + virtomird with PAM (cgo).
#   scripts/build-deb.sh 0.2.0             → out/virtomir_0.2.0_<arch>.deb (+ .sha256)
#   SKIP_WEB=1 scripts/build-deb.sh 0.2.0  → reuse an existing web/dist
# Needs: go, node/npm (unless SKIP_WEB), gcc, libpam0g-dev, dpkg-deb.
#
# Installs: /usr/bin/virtomird, the systemd unit virtomird.service (enabled and started),
# /etc/pam.d/virtomir and /etc/default/virtomird (listen address, flags). Settings live in
# /var/lib/virtomir and survive upgrades; "apt purge virtomir" deletes them.
set -eu

VERSION=${1:-$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo 0.0.0)}
VERSION=$(printf %s "$VERSION" | sed 's/^v//; s/-/~/g') # Debian: "~" sorts before the release
ROOT=$(cd "$(dirname "$0")/.." && pwd)
ARCH=$(dpkg --print-architecture)
NAME="virtomir_${VERSION}_${ARCH}"
OUT=${OUT:-$ROOT/out} # set OUT to a Linux filesystem when the tree is on NTFS (WSL)
PKG="$OUT/$NAME"

if [ "${SKIP_WEB:-0}" != 1 ]; then
	echo "==> web UI"
	(cd "$ROOT/web" && npm ci --no-fund --no-audit && npm run build)
fi
[ -f "$ROOT/web/dist/index.html" ] || { echo "web/dist is empty: build the UI first" >&2; exit 1; }

echo "==> virtomird $VERSION ($ARCH, PAM)"
rm -rf "$PKG"
mkdir -p "$OUT" "$PKG/DEBIAN" "$PKG/usr/bin" "$PKG/lib/systemd/system" "$PKG/etc/pam.d" "$PKG/etc/default" "$PKG/usr/share/doc/virtomir"
(cd "$ROOT" && CGO_ENABLED=1 go build -tags pam -trimpath \
	-ldflags "-s -w -X main.version=$VERSION" -o "$PKG/usr/bin/virtomird" ./cmd/virtomird)

cp "$ROOT/packaging/virtomir.pam" "$PKG/etc/pam.d/virtomir"
cat >"$PKG/etc/default/virtomird" <<'EOF'
# Flags for virtomird (see "virtomird -h"). Restart after changing:
#   systemctl restart virtomird
#
# -listen 127.0.0.1:8420   only local access (e.g. behind a reverse proxy)
# -tls files -tls-cert /etc/ssl/… -tls-key /etc/ssl/…   your own certificate
# -auth-group libvirt      members of this group (and root) are admins
# -update-repo ""          turn off the update check (Account → Updates)
VIRTOMIRD_ARGS="-listen 0.0.0.0:8420"
# Only for a private release repository: a GitHub token that may read it.
#VIRTOMIR_GITHUB_TOKEN=
EOF

cat >"$PKG/lib/systemd/system/virtomird.service" <<'EOF'
[Unit]
Description=VirtoMir - web control deck for libvirt
Documentation=https://github.com/onurbaydas/VirtoMir
After=network-online.target libvirtd.service virtqemud.service
Wants=network-online.target

[Service]
# Runs as root: it talks to the system libvirt socket, checks passwords via PAM and, for HA,
# may use the hardware watchdog and the BMC tools.
EnvironmentFile=-/etc/default/virtomird
ExecStart=/usr/bin/virtomird $VIRTOMIRD_ARGS
StateDirectory=virtomir
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
ProtectHome=read-only
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

cp "$ROOT/README.md" "$PKG/usr/share/doc/virtomir/README.md"
cp "$ROOT/LICENSE" "$PKG/usr/share/doc/virtomir/LICENSE"
cat >"$PKG/usr/share/doc/virtomir/copyright" <<'EOF'
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: VirtoMir
Source: https://github.com/onurbaydas/VirtoMir

Files: *
Copyright: Onur Baydas
License: AGPL-3.0-or-later
 On Debian systems the full text is in /usr/share/common-licenses/AGPL-3 if present,
 and at https://www.gnu.org/licenses/agpl-3.0.html.
EOF

SIZE=$(du -sk "$PKG" | cut -f1)
cat >"$PKG/DEBIAN/control" <<EOF
Package: virtomir
Version: $VERSION
Architecture: $ARCH
Maintainer: Onur Baydas <41346713+onurbaydas@users.noreply.github.com>
Installed-Size: $SIZE
Section: admin
Priority: optional
Homepage: https://github.com/onurbaydas/VirtoMir
Depends: libc6 (>= 2.34), libpam0g, libpam-runtime, libvirt-daemon-system, qemu-system-x86 | qemu-system-arm, adduser
Recommends: qemu-utils, ovmf, swtpm, swtpm-tools, tcpdump, virtiofsd, restic
Suggests: ipmitool, nfs-common, open-iscsi, ceph-common, openvswitch-switch, lvm2, zfsutils-linux, libvirt-daemon-driver-storage-zfs
Description: detailed web control deck for KVM/QEMU virtual machines
 VirtoMir is a web UI and API for libvirt hosts: machines with consoles, snapshots,
 backups, templates and live metrics; storage, networks and passthrough; several
 hosts as one cluster with live migration and high availability.
 .
 It installs next to an existing libvirt; nothing about the host's distribution
 changes. Open https://<host>:8420/ and sign in with a Linux account in the libvirt
 group (admins) or virtomir-operator / virtomir-viewer.
EOF

printf '/etc/default/virtomird\n/etc/pam.d/virtomir\n' >"$PKG/DEBIAN/conffiles"

cat >"$PKG/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = configure ]; then
	for g in virtomir-operator virtomir-viewer; do
		getent group "$g" >/dev/null || addgroup --system --quiet "$g"
	done
	if [ -d /run/systemd/system ]; then
		systemctl daemon-reload
		systemctl enable virtomird.service >/dev/null 2>&1 || true
		if [ -n "$2" ]; then
			systemctl try-restart virtomird.service || true # upgrade
		else
			systemctl start virtomird.service || true
		fi
	fi
	addr=$(hostname -I 2>/dev/null | cut -d' ' -f1)
	echo "VirtoMir: open https://${addr:-<this-host>}:8420/ (self-signed certificate) and sign in"
	echo "          with a Linux account in the 'libvirt' group (or root)."
	if [ ! -S /run/libvirt/libvirt-sock ] && [ ! -S /var/run/libvirt/libvirt-sock ]; then
		echo "          libvirt is not running yet: systemctl enable --now libvirtd"
	fi
fi
exit 0
EOF

cat >"$PKG/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
if [ -d /run/systemd/system ] && [ "$1" = remove ]; then
	systemctl disable --now virtomird.service >/dev/null 2>&1 || true
fi
exit 0
EOF

cat >"$PKG/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -e
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
fi
if [ "$1" = purge ]; then
	# Settings, certificate, metric history, backups index. Virtual machines are not touched.
	rm -rf /var/lib/virtomir
fi
exit 0
EOF

chmod 0755 "$PKG/DEBIAN/postinst" "$PKG/DEBIAN/prerm" "$PKG/DEBIAN/postrm" "$PKG/usr/bin/virtomird"
find "$PKG" -type d -exec chmod 0755 {} +
find "$PKG/etc" "$PKG/lib" "$PKG/usr/share" -type f -exec chmod 0644 {} +

echo "==> package"
dpkg-deb --root-owner-group --build "$PKG" "$OUT/$NAME.deb" >/dev/null
(cd "$OUT" && sha256sum "$NAME.deb" >"$NAME.deb.sha256")
rm -rf "$PKG"
echo "==> $OUT/$NAME.deb"
