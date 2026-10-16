# AGENTS.md — Start Here

> This file is the **single entry point for any AI agent** (Antigravity, Claude Code, Codex, Cursor, Copilot, Gemini CLI, …) working on VirtoMir.
> Read it fully before doing anything. `CLAUDE.md` and `GEMINI.md` only point here.

## 1. What is VirtoMir?

**VirtoMir — "A Detailed Control Deck for Virtual Environments."**

A modern, extremely user-friendly **and** extremely detailed web control panel for **KVM / QEMU / libvirt**, installable on any existing Linux host (unlike Proxmox, which is a whole distribution) and far deeper than Cockpit or virt-manager.

## 2. Project memory

The maintainers keep the project memory (vision, decision log, roadmap, session notes) in a local, gitignored `.ai/` folder; it is not published. If it exists on your machine, read `.ai/context/vision.md`, `decisions.md`, `roadmap.md` and the newest `.ai/sessions/` note first, and **never contradict an `Accepted` decision silently**. Code comments refer to those decisions as `ADR-00xx`. Without it, start from the README and the code.

## 3. Working rules for agents

- **Language:** Reply to the maintainer in the language they write in. Code, comments, commit messages and docs are written in **English**.
- **Update memory as you work** (when `.ai/` exists): new decisions go to `decisions.md` with the next ADR number; end each working session with a note in `.ai/sessions/`. Nothing under `.ai/` is ever committed.
- **Secrets:** Never commit credentials, tokens, IPs with passwords, chat transcripts or personal notes. Local-only material goes in `.ai/` (gitignored).
- **Licenses:** Learning from other open-source projects is encouraged. Copying **code** requires license compatibility (e.g. virt-manager is GPL-2.0+, Cockpit is LGPL-2.1+). Note the source + license in the file header and in `decisions.md` when code is borrowed.
- **Commits:** Conventional Commits (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:` …). Keep `.ai/` updates in the same commit as the work they describe, or in a separate `docs(ai):` commit.
- **Product principles override convenience.** If an implementation shortcut hurts user-friendliness, visual quality, monitoring accuracy or console reliability, don't take it.

## 4. Repository layout

```
AGENTS.md            ← you are here (CLAUDE.md / GEMINI.md point here)
README.md            ← public-facing project README
.ai/                 ← gitignored, local only: project memory and secrets (not published)
scripts/             ← build/release/install scripts, dev-run.ps1
dev/hyperv/          ← one-command Hyper-V dev host (Debian 13 + KVM/libvirt)
cmd/virtomird/       ← daemon entry point (Go, ADR-0006)
internal/virt/       ← libvirt gateway: connection, reconnect, libvirt → VirtoMir types
                       detail.go: domain XML (libvirtxml) → machine page model, fixtures in testdata/
                       manage/devices/snapshots/storage/networks/create.go: the management operations (ADR-0011)
                       console.go (ADR-0016), backup.go (ADR-0018), template.go: templates, linked clones, <metadata> (ADR-0019)
                       move.go / migrate.go: cold move + live migration, cpucompat.go: CPU baseline, ova.go: OVA import,
                       tuning.go: pinning/hugepages/NUMA, hostdev.go: PCI/USB passthrough + IOMMU safety checks,
                       extradevs.go: watchdog/TPM/RNG/virtiofs/mdev/clipboard/video + QEMU args, mdev.go: host vGPU slices (ADR-0025)
internal/live/       ← 1 s sampling hub + 60 s backlog, per-VM bulk stats, host /proc I/O (SSE)
internal/api/        ← HTTP/JSON API (/api/v1), sign-in guard, SSE stream, serves the embedded UI
internal/auth/       ← PAM (build tag `pam`), sessions, lockout, self-signed TLS (ADR-0010), roles + API tokens (ADR-0015),
                       external.go: LDAP + OIDC users and signin.json (ADR-0023; OIDC flow in internal/api/oidc.go)
internal/confighist/ ← every version of each machine's definition, diff + revert (ADR-0021)
internal/quota/      ← per-user/group quotas, machine ownership, operator self-service (ADR-0022)
internal/activity/   ← who did what: -state-dir/activity.jsonl (ADR-0015)
internal/alerts/     ← alert rules on live snapshots, webhook/e-mail channels (ADR-0017)
internal/schedule/   ← automatic snapshots/backups with retention (ADR-0018)
internal/cluster/    ← multi-host (ADR-0020): member list, join codes, pinned peer HTTPS + peer keys;
                       forwarding of /api/v1/hosts/<id>/… lives in internal/api/cluster.go
internal/jobs/       ← background jobs (clone, backup, restore, deploy) the UI polls
internal/history/    ← metric history rings, saved to -state-dir/history.gob (ADR-0013)
packaging/           ← /etc/pam.d/virtomir, systemd unit
.github/workflows/   ← CI (tests, race, PAM build, shellcheck) and releases on v* tags
scripts/install.sh   ← installer shipped in release tarballs; build-release.sh builds them
docs/screenshots/    ← README images (taken from the mock UI)
web/                 ← Svelte 5 + Vite + TS UI (ADR-0007); `web/dist` is embedded into virtomird
  src/app.css        ← design tokens (colors, radii, motion) — use these, never hard-coded colors
  src/lib/           ← api client, live/session stores, router, components (Dialog, ConfirmButton, …)
  src/lib/pages/     ← Overview, MachinePage (#console|#settings|#hardware|#snapshots|#backups|#changes|#xml), NewMachine,
                       Import, Storage, Networks, Hardware (host PCI/USB), Backups, Alerts, Activity, Cluster,
                       Account (quotas, sign-in settings), Console (full-screen /console/<uuid>), Login
```

## 5. Build, test, run

```powershell
go test ./...                 # Go unit tests (run anywhere, no libvirt needed)
cd web; npm run check         # Svelte/TS type check
./scripts/dev-run.ps1         # build UI + daemon, deploy, run WITHOUT sign-in over http (-auth none -tls off)
./scripts/dev-run.ps1 -Pam    # build on the dev host with PAM, run with sign-in + self-signed TLS
# UI: http(s)://192.168.77.10:8420/  ·  logs: ssh virtomir-dev journalctl -u virtomird-dev -f
cd web; npm run dev           # hot-reload UI on Windows, proxies /api to the dev host
```

Race tests need cgo → run them on the dev host. Go is installed per-user at `%LOCALAPPDATA%\Programs\go` on Windows and at `/usr/local/go` on the dev host.
