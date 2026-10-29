#!/bin/sh
# Builds a release tarball on Linux: web UI + virtomird with PAM (cgo).
#   scripts/build-release.sh v0.1.0      → out/virtomir_v0.1.0_linux_<arch>.tar.gz (+ .sha256)
# Needs: go, node/npm, gcc, libpam0g-dev (Debian/Ubuntu) or pam-devel (Fedora/RHEL).
set -eu

VERSION=${1:-$(git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
ARCH=$(go env GOARCH)
NAME="virtomir_${VERSION}_linux_${ARCH}"
OUT="$ROOT/out"
STAGE="$OUT/$NAME"

echo "==> web UI"
(cd "$ROOT/web" && npm ci --no-fund --no-audit && npm run build)

echo "==> virtomird $VERSION ($ARCH, PAM)"
rm -rf "$STAGE"
mkdir -p "$STAGE"
(cd "$ROOT" && CGO_ENABLED=1 go build -tags pam -trimpath \
	-ldflags "-s -w -X main.version=$VERSION" -o "$STAGE/virtomird" ./cmd/virtomird)

echo "==> package"
cp "$ROOT/packaging/virtomird.service" "$ROOT/packaging/virtomir.pam" "$ROOT/packaging/virtomir.cil" "$STAGE/"
cp "$ROOT/scripts/install.sh" "$STAGE/"
cp "$ROOT/README.md" "$STAGE/"
[ -f "$ROOT/LICENSE" ] && cp "$ROOT/LICENSE" "$STAGE/"
chmod 0755 "$STAGE/virtomird" "$STAGE/install.sh"
tar -C "$OUT" -czf "$OUT/$NAME.tar.gz" "$NAME"
(cd "$OUT" && sha256sum "$NAME.tar.gz" > "$NAME.tar.gz.sha256")
rm -rf "$STAGE"
echo "==> $OUT/$NAME.tar.gz"
