#!/bin/sh
# EXPERIMENTAL: builds an RPM (Fedora, RHEL/Rocky/Alma 10) from the same binary as the .deb.
#   scripts/build-rpm.sh 0.5.0             → out/virtomir-0.5.0-1.<arch>.rpm (+ .sha256)
#   SKIP_WEB=1 scripts/build-rpm.sh 0.5.0  → reuse an existing web/dist
# Needs: go, node/npm (unless SKIP_WEB), gcc, PAM headers, rpmbuild (Debian/Ubuntu: apt install rpm).
#
# The binary links against the build host's glibc: built on Ubuntu 24.04 it needs glibc 2.39
# (Fedora 40+, RHEL 10). In-app updates only install .deb packages; update RPM hosts with dnf.
set -eu

VERSION=${1:-$(git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo 0.0.0)}
VERSION=$(printf %s "$VERSION" | sed 's/^v//; s/-/~/g') # RPM: "~" sorts before the release
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=${OUT:-$ROOT/out}
case $(go env GOARCH) in
amd64) ARCH=x86_64 ;;
arm64) ARCH=aarch64 ;;
*) echo "unsupported architecture $(go env GOARCH)" >&2; exit 1 ;;
esac
TOP=$(mktemp -d)
trap 'rm -rf "$TOP"' EXIT

if [ "${SKIP_WEB:-0}" != 1 ]; then
	echo "==> web UI"
	(cd "$ROOT/web" && npm ci --no-fund --no-audit && npm run build)
fi
[ -f "$ROOT/web/dist/index.html" ] || { echo "web/dist is empty: build the UI first" >&2; exit 1; }

echo "==> virtomird $VERSION ($ARCH, PAM)"
SRC="$TOP/files"
mkdir -p "$SRC" "$OUT"
(cd "$ROOT" && CGO_ENABLED=1 go build -tags pam -trimpath \
	-ldflags "-s -w -X main.version=$VERSION" -o "$SRC/virtomird" ./cmd/virtomird)

# Fedora/RHEL PAM stack: system-auth instead of Debian's common-auth.
cat >"$SRC/virtomir.pam" <<'EOF'
#%PAM-1.0
# PAM service for VirtoMir sign-in.
auth       include      system-auth
account    include      system-auth
EOF
sed 's|ExecStart=/usr/local/bin/virtomird -listen 0.0.0.0:8420|EnvironmentFile=-/etc/sysconfig/virtomird\nExecStart=/usr/bin/virtomird $VIRTOMIRD_ARGS|' \
	"$ROOT/packaging/virtomird.service" >"$SRC/virtomird.service"
cat >"$SRC/virtomird.sysconfig" <<'EOF'
# Flags for virtomird (see "virtomird -h"). Restart after changing: systemctl restart virtomird
VIRTOMIRD_ARGS="-listen 0.0.0.0:8420"
EOF
cp "$ROOT/README.md" "$ROOT/LICENSE" "$SRC/"

cat >"$TOP/virtomir.spec" <<EOF
Name:           virtomir
Version:        $VERSION
Release:        1
Summary:        Detailed web control deck for KVM/QEMU virtual machines (experimental package)
License:        AGPL-3.0-or-later
URL:            https://github.com/onurbaydas/VirtoMir
Requires:       pam, libvirt-daemon-kvm, qemu-kvm, systemd
Recommends:     qemu-img, edk2-ovmf, swtpm, swtpm-tools, tcpdump, virtiofsd
%global debug_package %{nil}
%global __strip /bin/true

%description
VirtoMir is a web UI and API for libvirt hosts: machines with consoles, snapshots,
backups, templates and live metrics; storage, networks and passthrough; several hosts
as one cluster with live migration and high availability.

%install
install -Dm0755 $SRC/virtomird %{buildroot}/usr/bin/virtomird
install -Dm0644 $SRC/virtomird.service %{buildroot}/usr/lib/systemd/system/virtomird.service
install -Dm0644 $SRC/virtomir.pam %{buildroot}/etc/pam.d/virtomir
install -Dm0644 $SRC/virtomird.sysconfig %{buildroot}/etc/sysconfig/virtomird
install -Dm0644 $SRC/README.md %{buildroot}/usr/share/doc/virtomir/README.md
install -Dm0644 $SRC/LICENSE %{buildroot}/usr/share/licenses/virtomir/LICENSE

%files
/usr/bin/virtomird
/usr/lib/systemd/system/virtomird.service
%config(noreplace) /etc/pam.d/virtomir
%config(noreplace) /etc/sysconfig/virtomird
/usr/share/doc/virtomir/README.md
/usr/share/licenses/virtomir/LICENSE

%post
for g in virtomir-operator virtomir-viewer; do getent group \$g >/dev/null || groupadd -r \$g; done
systemctl daemon-reload >/dev/null 2>&1 || :
if [ \$1 -eq 1 ]; then systemctl enable --now virtomird.service >/dev/null 2>&1 || :; fi
echo "VirtoMir: open https://<this-host>:8420/ and sign in with a Linux account in the 'libvirt' group (or root)."

%preun
if [ \$1 -eq 0 ]; then systemctl disable --now virtomird.service >/dev/null 2>&1 || :; fi

%postun
systemctl daemon-reload >/dev/null 2>&1 || :
if [ \$1 -ge 1 ]; then systemctl try-restart virtomird.service >/dev/null 2>&1 || :; fi
EOF

echo "==> package"
rpmbuild --quiet -bb --target "$ARCH" --define "_topdir $TOP/rpm" "$TOP/virtomir.spec"
RPM=$(find "$TOP/rpm/RPMS" -name '*.rpm' | head -n1)
NAME=$(basename "$RPM")
cp "$RPM" "$OUT/$NAME"
(cd "$OUT" && sha256sum "$NAME" >"$NAME.sha256")
echo "==> $OUT/$NAME"
