# AGENTS.md — Start Here

> This file is the **single entry point for any AI agent** (Antigravity, Claude Code, Codex, Cursor, Copilot, Gemini CLI, …) working on VirtoMir.
> Read it fully before doing anything. `CLAUDE.md` and `GEMINI.md` only point here.

## 1. What is VirtoMir?

**VirtoMir — "A Detailed Control Deck for Virtual Environments."**

A modern, extremely user-friendly **and** extremely detailed web control panel for **KVM / QEMU / libvirt**, installable on any existing Linux host (unlike Proxmox, which is a whole distribution) and far deeper than Cockpit or virt-manager.

Full vision and product principles: [`.ai/context/vision.md`](.ai/context/vision.md)

## 2. Project memory (mandatory reading order)

All project memory lives in the repo under [`.ai/`](.ai/README.md) so that **any agent on any machine** can continue the work.

1. [`.ai/context/vision.md`](.ai/context/vision.md) — what we build and the non-negotiable product principles
2. [`.ai/context/decisions.md`](.ai/context/decisions.md) — decision log (ADR style). **Never contradict an `Accepted` decision silently**; propose a new one instead.
3. [`.ai/context/open-questions.md`](.ai/context/open-questions.md) — things not decided yet
4. [`.ai/context/environment.md`](.ai/context/environment.md) — dev / test environment
5. [`.ai/sessions/`](.ai/sessions/) — newest session summaries first; read at least the latest one

## 3. Working rules for agents

- **Language:** Reply to the maintainer in the language they write in. Code, comments, commit messages and `.ai/` docs are written in **English**.
- **Update memory as you work:**
  - New decision → append to `decisions.md` (next ADR number, with status).
  - Resolved question → move it from `open-questions.md` into `decisions.md`.
  - At the end of every working session → add `.ai/sessions/YYYY-MM-DD_<topic>_<agent>.md` (template in [`.ai/README.md`](.ai/README.md)).
- **Secrets:** Never commit credentials, tokens, IPs with passwords, etc. Local-only secrets go in `.ai/local/` (gitignored). Run transcript sync only through `scripts/sync-ai-memory.ps1`, which redacts secrets.
- **Licenses:** Learning from other open-source projects is encouraged. Copying **code** requires license compatibility (e.g. virt-manager is GPL-2.0+, Cockpit is LGPL-2.1+). Note the source + license in the file header and in `decisions.md` when code is borrowed.
- **Commits:** Conventional Commits (`feat:`, `fix:`, `docs:`, `chore:`, `refactor:` …). Keep `.ai/` updates in the same commit as the work they describe, or in a separate `docs(ai):` commit.
- **Product principles override convenience.** If an implementation shortcut hurts user-friendliness, visual quality, monitoring accuracy or console reliability, don't take it.

## 4. Repository layout

```
AGENTS.md            ← you are here (CLAUDE.md / GEMINI.md point here)
README.md            ← public-facing project README
.ai/                 ← shared AI memory (see .ai/README.md)
  context/           ← vision, decisions, open questions, environment
  sessions/          ← per-session summaries + redacted raw transcripts
  local/             ← gitignored: secrets, machine-specific notes
scripts/             ← helper scripts (sync-ai-memory.ps1, dev-run.ps1)
dev/hyperv/          ← one-command Hyper-V dev host (Debian 13 + KVM/libvirt)
cmd/virtomird/       ← daemon entry point (Go, ADR-0006)
internal/virt/       ← libvirt gateway: connection, reconnect, libvirt → VirtoMir types
internal/api/        ← HTTP/JSON API, versioned under /api/v1
```

## 5. Build, test, run

```powershell
go test ./...                 # unit tests (run anywhere, no libvirt needed)
./scripts/dev-run.ps1         # cross-compile, deploy to the dev host, (re)start as systemd unit virtomird-dev
# then: http://192.168.77.10:8420/api/v1/domains  ·  logs: ssh virtomir-dev journalctl -u virtomird-dev -f
```

Go is installed per-user at `%LOCALAPPDATA%\Programs\go` on Windows and at `/usr/local/go` on the dev host.
