#!/bin/sh
# Writes the virtomird(8) manual page from the flags the binary itself prints with -h, so the
# page never drifts from the code.
#   scripts/manpage.sh ./virtomird 1.2.0 > virtomird.8
set -eu
BIN=$1
VERSION=${2:-dev}
DATE=$(date -u +%Y-%m-%d)

cat <<EOF
.TH VIRTOMIRD 8 "$DATE" "VirtoMir $VERSION" "System Administration"
.SH NAME
virtomird \\- VirtoMir, a web control panel for KVM, QEMU and libvirt
.SH SYNOPSIS
.B virtomird
[\\fIflags\\fR]
.SH DESCRIPTION
.B virtomird
serves VirtoMir's web interface and its REST API (/api/v1) on one HTTPS port and manages the
virtual machines, storage and networks of the libvirt on this host. Admins sign in with their
Linux accounts (PAM); the systemd unit
.I virtomird.service
starts it. Open https://\\fIhost\\fR:8420/ in a browser.
.SH OPTIONS
EOF
"$BIN" -h 2>&1 | awk '
	/^  -/ { name = $1; sub(/^-/, "", name); type = $2; next }
	/^ *\t/ && name != "" {
		sub(/^ *\t/, "")
		gsub(/-/, "\\-", name)
		printf ".TP\n.B \\-%s", name
		if (type != "") printf " \\fI%s\\fR", type
		printf "\n%s\n", $0
		name = ""; type = ""
	}' | sed -e '/^\.TP$/b' -e '/^\.B /b' -e 's/\\/\\e/g' -e 's/^\./\\\&./' -e 's/-/\\-/g'
cat <<'EOF'
.SH FILES
.TP
.I /var/lib/virtomir
State directory (\-state\-dir): TLS certificate, metric history, alerts, schedules, activity
log, API tokens, cluster membership, configuration history, quotas and sign-in settings; the
default backup location is its backups/ folder.
.TP
.I /etc/default/virtomird \fRor\fP /etc/sysconfig/virtomird
Flags for the systemd unit (VIRTOMIRD_ARGS); run
.B systemctl restart virtomird
after a change.
.TP
.I /etc/pam.d/virtomir
PAM service used for sign-in (\-pam\-service).
.SH EXAMPLES
Listen on all addresses with the self-signed certificate:
.PP
.RS
VIRTOMIRD_ARGS="\-listen 0.0.0.0:8420"
.RE
.SH SEE ALSO
.BR virsh (1),
.BR libvirtd (8),
.BR systemctl (1)
.PP
https://github.com/onurbaydas/VirtoMir
EOF
