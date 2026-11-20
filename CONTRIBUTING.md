# Contributing to VirtoMir

Thank you for helping! Testing on your hardware, reporting bugs, translating and code are all
equally welcome. You do not need to know Go or Svelte to make a difference.

## Finding something to work on

1. **Pick a [good first issue](https://github.com/onurbaydas/VirtoMir/labels/good%20first%20issue).**
   Small, well-scoped tasks (docs, translation, frontend badges, small CLI flags) that explain which files to touch and how to verify your changes.
2. **Tackle a [help wanted](https://github.com/onurbaydas/VirtoMir/labels/help%20wanted) feature.**
   Architectural enhancements, hardware integrations (Redfish/IPMI, SR-IOV), storage backends, and IaC modules tracked under [Milestone v0.7.0](https://github.com/onurbaydas/VirtoMir/milestone/1).
3. **Claim an issue:** Before writing code, drop a short comment on the issue saying you would like to work on it. This avoids duplicate efforts and lets us align early on design.
4. **Test it and tell us.** [Try VirtoMir](docs/try-in-a-vm.md) (the online demo, or a throwaway
   VM in one command) and open a
   [test report](https://github.com/onurbaydas/VirtoMir/issues/new?template=test_report.yml) (or join
   [pinned issue #1](https://github.com/onurbaydas/VirtoMir/issues/1)).
   Reports from setups we cannot reach — other distributions, real BMCs for HA fencing, Ceph,
   iSCSI, GPUs — are gold.
5. **Translate.** The UI is English and Turkish; see [Translations](#translations).

Questions and ideas: [Discussions](https://github.com/onurbaydas/VirtoMir/discussions).
Security problems: report them privately (repository → Security → Report a vulnerability),
never in a public issue.

## Getting the code running

You need Go (the version in `go.mod`) and Node.js 22 or newer. No libvirt is needed for the
tests or for working on the UI.

```sh
git clone https://github.com/onurbaydas/VirtoMir && cd VirtoMir
go test ./...                        # Go unit tests, run anywhere
cd web && npm ci
npm run check && npm test            # TypeScript/Svelte checks and UI tests
npm run dev:demo                     # the UI with a pretend API: http://localhost:5173
```

`npm run dev:demo` is the fastest way to work on the interface: the whole UI runs against the
in-browser demo backend in `web/src/lib/demo/` (the same one as the
[online demo](https://onurbaydas.github.io/VirtoMir/demo/)). To work against a real host, run
`virtomird` there and point `npm run dev` at it with `VIRTOMIR_API=https://<host>:8420`.

To run the daemon itself you need a Linux host with libvirt; a throwaway VM from
[docs/try-in-a-vm.md](docs/try-in-a-vm.md) works well. Build it with:

```sh
(cd web && npm run build)                                      # the UI is embedded into the binary
CGO_ENABLED=1 go build -tags pam -o virtomird ./cmd/virtomird  # needs gcc + libpam0g-dev
sudo ./virtomird -listen 0.0.0.0:8420
```

Without `-tags pam` it builds anywhere but runs only with `-auth none` (development only:
anyone who can reach it controls every machine).

## Where things are

The full map is in [AGENTS.md](AGENTS.md) (written for AI coding agents, but it is the best
overview for people too). In short: `cmd/virtomird` is the daemon, `internal/virt` talks to
libvirt, `internal/api` is the HTTP API, `web/` is the Svelte UI.

## How we work

- **Small pull requests** with a clear "what and why" are reviewed fastest. For something big,
  open an issue or discussion first so we can agree on the shape.
- **Commits** follow [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`,
  `docs:`, `refactor:`, `test:`, `chore:`.
- **Code, comments and docs are in English.** Issues and discussions may be in English or Turkish.
- **Tests:** new behaviour in `internal/` comes with a Go test; UI helpers with a Vitest test.
- **The UI never hard-codes colors:** use the tokens in `web/src/app.css`.
- **Borrowing code** from other projects needs a compatible license (VirtoMir is AGPL-3.0-or-later);
  name the source and license in the file header.
- **AI tools are fine** to use. You are responsible for every line you submit: read it, test it,
  and be ready to explain it.

## Translations

Every UI string is written as `t('English text')`, and the English text is the key. A language
is one file in `web/src/lib/locales/` mapping English keys to translations (see `tr.ts`, kept
sorted). To add a language:

1. Copy `tr.ts` to `<code>.ts` (e.g. `de.ts`) and translate the values; leave `{placeholders}` as they are.
2. Add it to `LANGS` and `DICTS` in `web/src/lib/i18n.svelte.ts`.
3. `npm test` checks that placeholders match.

Untranslated keys fall back to English, so a partial translation is still useful.

The README's front page is translated too, in [`docs/i18n`](docs/i18n) (one `README.<code>.md` per
language, linked from the language bar at the top of every version). Those files are generated: the
texts live in [`scripts/readme-i18n`](scripts/readme-i18n) (`<code>.py` and `extra.py`). To correct a
translation, change the text there and run `python3 scripts/readme-i18n/gen.py <code>`. To add a
language, copy one of the `<code>.py` files, translate it, add it to `LANGS` in `gen.py` and to
`extra.py`, run the script for every language, and add the language to the bar in `README.md`. Corrections by native speakers are
very welcome: the current translations were written with an AI and may read stiffly.

## License and CLA

VirtoMir is licensed under the [GNU AGPL v3.0 or later](LICENSE), and is also offered under
separate commercial licenses. So that both stay possible, contributors sign a short
[Contributor License Agreement](CLA.md) once: on your first pull request a bot asks you to
reply with one sentence, and that is all. You keep the copyright in your work; every version
released under the AGPL stays under the AGPL.

Contributing for your employer? Make sure you are allowed to (see section 4 of the CLA).
