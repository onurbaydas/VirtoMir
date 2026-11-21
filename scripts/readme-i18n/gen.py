#!/usr/bin/env python3
"""Writes docs/i18n/README.<lang>.md from one template (below) and the texts in <lang>.py and
extra.py. Change the template and the texts, never the generated files; then run

    python3 scripts/readme-i18n/gen.py          # every language
    python3 scripts/readme-i18n/gen.py de fr    # some

The release commit bumps the version in the install lines here too."""
import importlib.util, pathlib, sys

sys.dont_write_bytecode = True

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parent.parent
sys.path.insert(0, str(HERE))

LANGS = [  # code, native name
    ("tr", "Türkçe"), ("de", "Deutsch"), ("fr", "Français"), ("es", "Español"), ("pt-BR", "Português"),
    ("it", "Italiano"), ("pl", "Polski"), ("nl", "Nederlands"), ("ru", "Русский"), ("uk", "Українська"),
    ("zh-CN", "简体中文"), ("zh-TW", "繁體中文"), ("ja", "日本語"), ("ko", "한국어"), ("ar", "العربية"),
]

def load(code):
    p = HERE / f"{code}.py"
    spec = importlib.util.spec_from_file_location(code.replace("-", "_"), p)
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m.T

def langbar(current):
    out = ['<a href="../../README.md">English</a>']
    for code, name in LANGS:
        out.append(name if code == current else f'<a href="README.{code}.md">{name}</a>')
    return " ·\n".join(out)

TEMPLATE = '''<p align="center"><img src="../brand/logo.svg" alt="VirtoMir logo" width="112" height="112"></p>

<h1 align="center">VirtoMir</h1>

<p align="center"><strong>A Detailed Control Deck for Virtual Environments.</strong><br>
{tagline}</p>

<p align="center">
{langbar}
</p>

<p align="center">
  <a href="https://github.com/onurbaydas/VirtoMir/releases/latest"><img src="https://img.shields.io/github/v/release/onurbaydas/VirtoMir?style=flat&color=e8b931&label=release" alt="release"></a>
  <a href="https://github.com/onurbaydas/VirtoMir/actions/workflows/ci.yml"><img src="https://github.com/onurbaydas/VirtoMir/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="../../LICENSE"><img src="https://img.shields.io/badge/License-AGPL_3.0-blue.svg" alt="AGPL-3.0"></a>
  <img src="https://img.shields.io/badge/Linux-amd64%20%C2%B7%20arm64-333?logo=linux&logoColor=white" alt="Linux amd64 · arm64">
  <a href="https://scorecard.dev/viewer/?uri=github.com/onurbaydas/VirtoMir"><img src="https://api.scorecard.dev/projects/github.com/onurbaydas/VirtoMir/badge" alt="OpenSSF Scorecard"></a>
  <a href="https://www.bestpractices.dev/projects/16980"><img src="https://www.bestpractices.dev/projects/16980/badge" alt="OpenSSF Best Practices"></a>
  <a href="https://registry.terraform.io/providers/onurbaydas/virtomir/latest"><img src="https://img.shields.io/badge/Terraform_Registry-onurbaydas%2Fvirtomir-7B42BC?logo=terraform&logoColor=white" alt="Terraform Registry"></a>
</p>

<p align="center">
  <a href="https://github.com/onurbaydas/VirtoMir/stargazers"><img src="https://img.shields.io/github/stars/onurbaydas/VirtoMir?style=social" alt="Star"></a>
  <a href="https://github.com/onurbaydas/VirtoMir/fork"><img src="https://img.shields.io/github/forks/onurbaydas/VirtoMir?style=social" alt="Fork"></a>
  <a href="https://github.com/onurbaydas/VirtoMir/subscription"><img src="https://img.shields.io/github/watchers/onurbaydas/VirtoMir?style=social" alt="Watch"></a>
</p>

<p align="center">
  <a href="https://onurbaydas.github.io/VirtoMir/demo/"><b>▶ {live_demo}</b></a> ·
  <a href="#install">{install}</a> ·
  <a href="../try-in-a-vm.md">{try_vm}</a> ·
  <a href="../features.md">{all_features}</a> ·
  <a href="#contribute">{contribute}</a> ·
  <a href="https://github.com/onurbaydas/VirtoMir/discussions">{discussions}</a>
</p>

<table align="center">
<tr><td align="center"><h3>1</h3></td><td align="center"><h3>0</h3></td><td align="center"><h3>0</h3></td><td align="center"><h3>90+</h3></td><td align="center"><h3>16</h3></td><td align="center"><h3>1 s</h3></td></tr>
<tr><td align="center"><sub>{s0}</sub></td><td align="center"><sub>{s1}</sub></td><td align="center"><sub>{s2}</sub></td><td align="center"><sub>{s3}</sub></td><td align="center"><sub>{s4}</sub></td><td align="center"><sub>{s5}</sub></td></tr>
</table>

> {note}

<p align="center"><img src="../screenshots/tour.gif" alt="VirtoMir" width="100%"></p>

{intro_lead}

- **{effortless_t}** {effortless_d}
- **{detailed_t}** {detailed_d}

{single_binary}

> **{status_t}** {status_d}

> [!TIP]
> {star_tip}

> [!NOTE]
> {feedback}

## {h_try}

| | |
|---|---|
| **{try_look_t}** | {try_look_d} |
| **{try_vm_t}** | {try_vm_d} |
| **Debian / Ubuntu** | `sudo apt install ./virtomir_1.2.2_amd64.deb` — [{latest_release}](https://github.com/onurbaydas/VirtoMir/releases/latest) |
| **Fedora / RHEL** | `sudo dnf install ./virtomir-1.2.2-1.x86_64.rpm` — [{latest_release}](https://github.com/onurbaydas/VirtoMir/releases/latest) |

{try_pkg_note}

## {h_why}

| | **VirtoMir** | Cockpit Machines | virt-manager | Proxmox VE |
|---|:---:|:---:|:---:|:---:|
| {why_1} | ✅ | ✅ | ✅ | ❌ {why_is_os} |
| {why_2} | ✅ | ✅ | ❌ {why_desktop} | ✅ |
| {why_3} | ✅ | ➖ {why_basic} | ✅ | ➖ {why_not_libvirt} |
| {why_4} | ✅ | ❌ | ❌ | ❌ |
| {why_5} | ✅ | ➖ | ❌ | ✅ |
| {why_6} | ✅ | ➖ {why_snapshots} | ➖ {why_snapshots} | ✅ |
| {why_7} | ✅ | ❌ | ➖ {why_migration} | ✅ |

{why_summary}

## {h_highlights}

<table>
<tr>
<td width="50%" valign="top">
<h3>🖥️ {hl1_t}</h3>
{hl1_d}
<br><br><img src="../screenshots/machine-performance.png" alt="{hl1_t}">
</td>
<td width="50%" valign="top">
<h3>🕰️ {hl2_t}</h3>
{hl2_d}
<br><br><img src="../screenshots/timeline.png" alt="{hl2_t}">
</td>
</tr>
<tr>
<td valign="top">
<h3>💾 {hl3_t}</h3>
{hl3_d}
<br><br><img src="../screenshots/storage.png" alt="{hl3_t}">
</td>
<td valign="top">
<h3>🛟 {hl4_t}</h3>
{hl4_d}
<br><br><img src="../screenshots/backups.png" alt="{hl4_t}">
</td>
</tr>
<tr>
<td valign="top">
<h3>🌐 {hl5_t}</h3>
{hl5_d}
<br><br><img src="../screenshots/ha.png" alt="{hl5_t}">
</td>
<td valign="top">
<h3>🔐 {hl6_t}</h3>
{hl6_d}
<br><br><img src="../screenshots/updates.png" alt="{hl6_t}">
</td>
</tr>
</table>

{more_features}

## {h_how}

```mermaid
flowchart LR
  user["Browser · REST API<br/>Ansible · Terraform"] -- "HTTPS :8420" --> d["virtomird"]
  d -- "libvirt socket" --> lv["libvirt"]
  lv --> vm["QEMU/KVM · LXC"]
  lv --> st["dir · LVM · ZFS · NFS · iSCSI · Ceph"]
  lv --> net["NAT · bridge · VLAN · VXLAN"]
  d -- "PAM · LDAP · OIDC" --> auth["Sign-in"]
  d <-- "HTTPS + join code" --> peer["virtomird (other hosts)"]
  d -- "/api/v1/metrics" --> prom["Prometheus · Grafana"]
```

- {how_1}
- {how_2}
- {how_3}
- {how_4}

<a id="install"></a>
## {h_install}

```sh
# Debian / Ubuntu
sudo install -d -m 0755 /etc/apt/keyrings
sudo curl -fsSL -o /etc/apt/keyrings/virtomir.asc https://onurbaydas.github.io/VirtoMir/virtomir.asc
echo "deb [signed-by=/etc/apt/keyrings/virtomir.asc] https://onurbaydas.github.io/VirtoMir/apt stable main" | sudo tee /etc/apt/sources.list.d/virtomir.list
sudo apt update && sudo apt install virtomir

# Fedora / RHEL 10
sudo curl -fsSL -o /etc/yum.repos.d/virtomir.repo https://onurbaydas.github.io/VirtoMir/virtomir.repo
sudo dnf install virtomir
```

{install_text}

{install_more}

{security}

<a id="contribute"></a>
## {h_help}

{help_intro}

- 🧪 {help_1}
- 🔍 {help_2}
- 🔧 {help_3}
- 🌍 {help_4}
- 💻 {help_5}

{help_links}

## {h_thanks}

<p><img src="../thanks/mega-bilgisayar.png" alt="Mega Bilgisayar" width="170"><br>{thanks}</p>

<p align="center">
  <b>{star_footer}</b><br><br>
  <a href="https://github.com/onurbaydas/VirtoMir/stargazers"><img src="https://img.shields.io/github/stars/onurbaydas/VirtoMir?style=for-the-badge&logo=github&color=e8b931&label=Star" alt="Star"></a>
  <a href="https://github.com/onurbaydas/VirtoMir/fork"><img src="https://img.shields.io/github/forks/onurbaydas/VirtoMir?style=for-the-badge&logo=github&color=3b5bdb&label=Fork" alt="Fork"></a>
  <a href="https://github.com/onurbaydas/VirtoMir/subscription"><img src="https://img.shields.io/github/watchers/onurbaydas/VirtoMir?style=for-the-badge&logo=github&color=2f9e44&label=Watch" alt="Watch"></a>
</p>

## {h_license}

{license}

Copyright © 2026 Onur Baydas. {license_commercial}
'''

def render(code, T):
    from extra import X, FEEDBACK, NO_ACCOUNT, GITHUB_ALT
    tip, footer, stats, contribute = X[code]
    form = "https://tally.so/r/db904z"
    github = "https://github.com/onurbaydas/VirtoMir/issues/new?template=quick_feedback.yml"
    fb = FEEDBACK[code].replace("{u}", form)
    head, sep, rest = fb.partition("**")  # "**<first sentence>** rest": the first sentence gets "(no account)"
    first, sep2, rest = rest.partition("**")
    fb = f"{head}**{first} ({NO_ACCOUNT[code]})**{rest} " + GITHUB_ALT[code].replace("{g}", github)
    extra = dict(star_tip=tip, star_footer=footer, contribute=contribute, feedback=fb,
                 **{f"s{i}": v for i, v in enumerate(stats)})
    body = TEMPLATE.format(langbar=langbar(code), **T, **extra)
    if code == "ar":
        body = '<div dir="rtl">\n\n' + body + '\n</div>\n'
    return body

out = ROOT / "docs" / "i18n"
out.mkdir(parents=True, exist_ok=True)
only = sys.argv[1:] or [c for c, _ in LANGS]
for code in only:
    T = load(code)
    (out / f"README.{code}.md").write_text(render(code, T))
    print("wrote", code)
