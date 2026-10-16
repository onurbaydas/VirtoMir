# onurbaydas.virtomir

Ansible collection for [VirtoMir](https://github.com/onurbaydas/VirtoMir) (ADR-0049). Module `machine`: create from a template or from scratch, resize (vCPUs, memory), start/stop, autostart, delete — idempotent, with check mode.

```yaml
- hosts: localhost
  collections: [onurbaydas.virtomir]
  module_defaults:
    group/onurbaydas.virtomir.all:
      url: https://hv1:8420
      token: "{{ lookup('env', 'VIRTOMIR_TOKEN') }}"
      validate_certs: false
  tasks:
    - name: A machine from a template
      machine: {name: web-1, template: deb13-tpl, linked: true, vcpus: 2, memory_mb: 2048, state: running}
```

Install: `ansible-galaxy collection build && ansible-galaxy collection install onurbaydas-virtomir-*.tar.gz`. License GPL-3.0-or-later.
