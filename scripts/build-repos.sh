#!/bin/sh
# Builds signed apt and rpm repositories from release packages (ADR-0048).
#   scripts/build-repos.sh <packages dir> <site dir>
# The site dir may already hold earlier releases (e.g. a checkout of the gh-pages branch):
# new packages are added, the indexes are rebuilt over everything, and older versions stay
# installable. Layout:
#   <site>/virtomir.asc                     public signing key
#   <site>/apt/pool/main/*.deb              apt: deb [signed-by=…] <url>/apt stable main
#   <site>/apt/dists/stable/{InRelease,Release,Release.gpg}, main/binary-{amd64,arm64}/Packages(.gz)
#   <site>/rpm/*.rpm, <site>/rpm/repodata/  dnf: <url>/virtomir.repo (gpgcheck + repo_gpgcheck)
#   <site>/index.html                       how to add the repository
# Environment:
#   GPG_KEY_ID   key to sign with (must be in the keyring; a passphrase-less subkey is easiest
#                in CI, or use GPG_PASSPHRASE)
#   REPO_URL     where the site is served, e.g. https://onurbaydas.github.io/VirtoMir
#   KEEP         how many versions per architecture stay in the repositories (default 5)
# Needs: gpg, apt-ftparchive (apt-utils), createrepo_c, rpmsign (rpm), gzip.
set -eu

PKGS=${1:?packages dir}
SITE=${2:?site dir}
: "${GPG_KEY_ID:?set GPG_KEY_ID to the signing key}"
REPO_URL=${REPO_URL:-https://onurbaydas.github.io/VirtoMir}
REPO_URL=${REPO_URL%/}
SUITE=stable
KEEP=${KEEP:-5}

prune() { # prune <dir> <glob>: keep the newest $KEEP files matching glob (by version order)
	# shellcheck disable=SC2012,SC2086 # $2 is a glob on purpose
	ls "$1"/$2 2>/dev/null | sort -V | head -n "-$KEEP" | while read -r old; do
		echo "    drop $(basename "$old")"
		rm -f "$old"
	done
}

gpg_sign() { # gpg_sign <args…>: sign non-interactively, with GPG_PASSPHRASE when set
	if [ -n "${GPG_PASSPHRASE:-}" ]; then
		printf %s "$GPG_PASSPHRASE" | gpg --batch --yes --pinentry-mode loopback --passphrase-fd 0 --local-user "$GPG_KEY_ID" "$@"
	else
		gpg --batch --yes --local-user "$GPG_KEY_ID" "$@"
	fi
}

mkdir -p "$SITE/apt/pool/main" "$SITE/rpm"
SITE=$(cd "$SITE" && pwd)
PKGS=$(cd "$PKGS" && pwd)
gpg --batch --yes --armor --export "$GPG_KEY_ID" >"$SITE/virtomir.asc"
[ -s "$SITE/virtomir.asc" ] || { echo "no public key for $GPG_KEY_ID" >&2; exit 1; }

# ---- apt ------------------------------------------------------------------------------
n=0
for deb in "$PKGS"/*.deb; do
	[ -e "$deb" ] || continue
	cp -f "$deb" "$SITE/apt/pool/main/"
	n=$((n + 1))
done
echo "==> apt: $n new package(s)"
for arch in amd64 arm64; do prune "$SITE/apt/pool/main" "virtomir_*_$arch.deb"; done
cd "$SITE/apt"
for arch in amd64 arm64; do
	dir="dists/$SUITE/main/binary-$arch"
	mkdir -p "$dir"
	apt-ftparchive --arch "$arch" packages pool/main >"$dir/Packages"
	gzip -9fk "$dir/Packages"
done
cat >"$SITE/apt-release.conf" <<EOF
APT::FTPArchive::Release::Origin "VirtoMir";
APT::FTPArchive::Release::Label "VirtoMir";
APT::FTPArchive::Release::Suite "$SUITE";
APT::FTPArchive::Release::Codename "$SUITE";
APT::FTPArchive::Release::Architectures "amd64 arm64";
APT::FTPArchive::Release::Components "main";
APT::FTPArchive::Release::Description "VirtoMir, a detailed control deck for KVM/QEMU/libvirt";
EOF
# The Release file lists every file under dists/<suite>: drop the old index and signatures
# first and write the new one outside, or it would list itself.
rm -f "dists/$SUITE/Release" "dists/$SUITE/InRelease" "dists/$SUITE/Release.gpg"
apt-ftparchive -c "$SITE/apt-release.conf" release "dists/$SUITE" >"$SITE/Release.tmp"
mv "$SITE/Release.tmp" "dists/$SUITE/Release"
rm -f "$SITE/apt-release.conf"
gpg_sign --clearsign -o "dists/$SUITE/InRelease" "dists/$SUITE/Release"
gpg_sign --armor --detach-sign -o "dists/$SUITE/Release.gpg" "dists/$SUITE/Release"
cd - >/dev/null

# ---- rpm ------------------------------------------------------------------------------
n=0
for rpm in "$PKGS"/*.rpm; do
	[ -e "$rpm" ] || continue
	cp -f "$rpm" "$SITE/rpm/"
	n=$((n + 1))
done
echo "==> rpm: $n new package(s)"
for arch in x86_64 aarch64; do prune "$SITE/rpm" "virtomir-*.$arch.rpm"; done
if ls "$SITE"/rpm/*.rpm >/dev/null 2>&1; then
	# Sign every package (re-signing an already signed one is harmless).
	extra="--batch"
	pf=""
	if [ -n "${GPG_PASSPHRASE:-}" ]; then
		pf=$(mktemp)
		printf %s "$GPG_PASSPHRASE" >"$pf"
		extra="--batch --pinentry-mode loopback --passphrase-file $pf"
	fi
	rpmsign --define "_gpg_name $GPG_KEY_ID" --define "__gpg $(command -v gpg)" \
		--define "_gpg_sign_cmd_extra_args $extra" --addsign "$SITE"/rpm/*.rpm >/dev/null
	[ -z "$pf" ] || rm -f "$pf"
	createrepo_c --quiet --update "$SITE/rpm"
	gpg_sign --armor --detach-sign -o "$SITE/rpm/repodata/repomd.xml.asc" "$SITE/rpm/repodata/repomd.xml"
fi
cat >"$SITE/virtomir.repo" <<EOF
[virtomir]
name=VirtoMir
baseurl=$REPO_URL/rpm/
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=$REPO_URL/virtomir.asc
EOF

touch "$SITE/.nojekyll" # served as plain files by GitHub Pages

# ---- how to use it --------------------------------------------------------------------
fpr=$(gpg --batch --with-colons --fingerprint "$GPG_KEY_ID" | awk -F: '/^fpr/ {print $10; exit}')
cat >"$SITE/index.html" <<EOF
<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>VirtoMir packages</title>
<style>body{font:15px/1.5 system-ui,sans-serif;max-width:760px;margin:40px auto;padding:0 16px;color:#1b1b1b;background:#f6f4ef}
pre{background:#111;color:#eee;padding:12px 14px;overflow:auto;border-radius:4px}code{font-family:ui-monospace,monospace}
@media (prefers-color-scheme:dark){body{background:#151515;color:#e8e6e1}}</style></head><body>
<h1>VirtoMir packages</h1>
<p>Signed repositories for <a href="https://github.com/onurbaydas/VirtoMir">VirtoMir</a>. Signing key fingerprint:
<code>$fpr</code> (<a href="virtomir.asc">virtomir.asc</a>).</p>
<h2>Debian / Ubuntu</h2>
<pre>sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL -o /etc/apt/keyrings/virtomir.asc $REPO_URL/virtomir.asc
echo "deb [signed-by=/etc/apt/keyrings/virtomir.asc] $REPO_URL/apt $SUITE main" | sudo tee /etc/apt/sources.list.d/virtomir.list
sudo apt update &amp;&amp; sudo apt install virtomir</pre>
<h2>Fedora / RHEL (experimental)</h2>
<pre>sudo curl -fsSL -o /etc/yum.repos.d/virtomir.repo $REPO_URL/virtomir.repo
sudo dnf install virtomir</pre>
<p>Updates then come with <code>apt upgrade</code> / <code>dnf upgrade</code>.</p>
</body></html>
EOF
echo "==> site ready in $SITE (key $fpr)"
