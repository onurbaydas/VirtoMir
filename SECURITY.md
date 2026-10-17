# Security policy

VirtoMir runs as root on hypervisors and controls every virtual machine on them, so security
reports are taken seriously and handled before anything else.

## Reporting a vulnerability

**Please do not open a public issue.** Report privately through GitHub:
**[Security → Report a vulnerability](https://github.com/onurbaydas/VirtoMir/security/advisories/new)**.

A useful report says:

- what an attacker can do, and from where (no account, a viewer or operator account, a cluster
  member, a guest inside a virtual machine, a file someone imports…);
- the VirtoMir version, and the libvirt/QEMU versions if they matter;
- steps or a proof of concept to reproduce it.

## What happens next

- You get an answer within **72 hours** confirming the report was received.
- Within **7 days** you hear whether it is confirmed and how serious it is.
- Fixes for serious issues are released as soon as they are ready, normally within **30 days**,
  with a GitHub security advisory. You are credited in the advisory unless you prefer not to be.
- Please give us the time to ship a fix before you publish details. If it takes longer than
  90 days, we will agree a date with you.

## Supported versions

VirtoMir is young and moves fast: security fixes go into the **latest release** only. Update
from the UI (Account → Updates) or with `apt upgrade` / `dnf upgrade`.

## In scope

Anything that crosses a boundary VirtoMir is meant to enforce, for example:

- signing in without valid credentials, or getting a higher role than granted;
- a viewer or operator changing things their role does not allow, or reading secrets (passwords,
  tokens, webhook URLs);
- a guest machine, an imported file (OVA, disk image, domain XML) or a cluster member reading or
  changing files on the host it should not;
- cross-site requests that change something on behalf of a signed-in user;
- a flaw in the update or package-signing process.

Not in scope: attacks that need root on the host already, or an admin account (admins can, by
design, change every machine and the host's network), and running with `-auth none`, which is
documented as development only.

## Hardening tips

- Keep VirtoMir on a management network, or listen on `127.0.0.1` behind your own reverse proxy.
- Give people the lowest role they need (viewer, operator, admin), and turn on two-step sign-in.
- Use API tokens with an expiry for scripts.
