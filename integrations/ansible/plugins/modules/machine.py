#!/usr/bin/python
# -*- coding: utf-8 -*-
# Copyright: (c) 2026, Onur Baydas
# GNU General Public License v3.0+ (see LICENSE or https://www.gnu.org/licenses/gpl-3.0.txt)

from __future__ import absolute_import, division, print_function

__metaclass__ = type

DOCUMENTATION = r'''
module: machine
short_description: Create, change, start, stop and delete VirtoMir machines
description:
  - A machine on a VirtoMir host, made from a template or from scratch (new disk, cloud image,
    installation ISO or an existing disk).
  - vCPUs, memory, power state and autostart are changed in place on an existing machine; the
    attributes that say how it was made only apply when it is created.
author: Onur Baydas (@onurbaydas)
options:
  name: {description: Machine name (or UUID of an existing one)., type: str, required: true}
  state:
    description: present (exists, power as it is), running, stopped, or absent.
    type: str
    choices: [present, running, stopped, absent]
    default: present
  template: {description: Template (name or UUID) to make the machine from., type: str}
  linked: {description: With template, a linked clone (instant, only its changes stored)., type: bool, default: false}
  os: {description: linux, windows or other., type: str, default: linux, choices: [linux, windows, other]}
  vcpus: {description: Virtual CPUs., type: int}
  memory_mb: {description: Memory in MiB., type: int}
  firmware: {description: uefi or bios (new machines)., type: str, choices: [uefi, bios]}
  pool: {description: Storage pool for a new disk or a copy of base_image., type: str}
  disk_gb: {description: Size of the new disk (or base_image grown to it)., type: int}
  base_image: {description: Path of a cloud image to copy., type: str}
  disk_path: {description: Existing disk image to boot as it is., type: str}
  iso: {description: Installation media to boot., type: str}
  network: {description: Virtual network for the adapter., type: str}
  bridge: {description: Host bridge for the adapter instead of a network., type: str}
  cloud_init:
    description: First-boot setup of a cloud image or template (user, password, ssh_keys, hostname, packages).
    type: dict
  autostart: {description: Start with the host., type: bool}
  delete_disks: {description: With state=absent, also delete its own disk images., type: bool, default: true}
  force: {description: Stop by pulling the plug instead of asking the guest first., type: bool, default: false}
  url: {description: VirtoMir address (or VIRTOMIR_URL)., type: str}
  token: {description: API token (or VIRTOMIR_TOKEN)., type: str}
  validate_certs: {description: Check the host's certificate., type: bool, default: true}
  host: {description: Cluster member to manage through url., type: str}
'''

EXAMPLES = r'''
- name: A machine from a template, running
  onurbaydas.virtomir.machine:
    name: web-1
    template: deb13-tpl
    linked: true
    vcpus: 2
    memory_mb: 2048
    cloud_init: {user: ops, ssh_keys: ["{{ lookup('file', '~/.ssh/id_ed25519.pub') }}"]}
    state: running

- name: Gone, with its disks
  onurbaydas.virtomir.machine: {name: web-1, state: absent}
'''

RETURN = r'''
machine:
  description: The machine as the API lists it (uuid, name, state, vcpus, maxMemoryKiB, autostart, …).
  returned: unless state=absent
  type: dict
'''

from ansible.module_utils.basic import AnsibleModule
from ansible.module_utils.six.moves.urllib.parse import quote

from ansible_collections.onurbaydas.virtomir.plugins.module_utils.virtomir import APIError, Client, common_args, is_running


def create(c, p):
    ci = p.get('cloud_init')
    if ci is not None:
        ci = dict(user=ci.get('user'), password=ci.get('password'), sshKeys=ci.get('ssh_keys') or [],
                  hostname=ci.get('hostname'), packages=ci.get('packages') or [])
        ci = dict((k, v) for k, v in ci.items() if v not in (None, [], ''))
    start = p['state'] in ('running', 'present')
    if p.get('template'):
        tpl = c.domain(p['template'])
        if tpl is None:
            raise APIError(404, 'template %s not found' % p['template'])
        body = dict(name=p['name'], linked=p['linked'], start=False)
        if ci:
            body['cloudInit'] = ci
        job = c.call('POST', '/api/v1/domains/%s/deploy' % quote(tpl['uuid'], safe=''), body)
        uuid = c.wait_job(job)['result']
        if p.get('vcpus') or p.get('memory_mb'):
            c.resize(uuid, p.get('vcpus'), p.get('memory_mb'))
        if start:
            c.action(uuid, 'start')
        return uuid
    spec = dict(name=p['name'], os=p['os'], vcpus=p.get('vcpus') or 1, memoryMiB=p.get('memory_mb') or 1024, start=start)
    for k, key in (('firmware', 'firmware'), ('pool', 'pool'), ('base_image', 'baseImage'), ('disk_path', 'diskPath'), ('iso', 'iso')):
        if p.get(k):
            spec[key] = p[k]
    if p.get('disk_gb'):
        spec['diskGiB'] = p['disk_gb']
    if ci:
        spec['cloudInit'] = ci
    if p.get('network') or p.get('bridge'):
        spec['network'] = dict((k, v) for k, v in (('network', p.get('network')), ('bridge', p.get('bridge'))) if v)
    res = c.call('POST', '/api/v1/domains?wait=true', spec)
    if res.get('startError'):
        raise APIError(500, 'created, but it did not start: %s' % res['startError'])
    return res['uuid']


def main():
    args = common_args()
    args.update(
        name=dict(type='str', required=True),
        state=dict(type='str', default='present', choices=['present', 'running', 'stopped', 'absent']),
        template=dict(type='str'), linked=dict(type='bool', default=False),
        os=dict(type='str', default='linux', choices=['linux', 'windows', 'other']),
        vcpus=dict(type='int'), memory_mb=dict(type='int'), firmware=dict(type='str', choices=['uefi', 'bios']),
        pool=dict(type='str'), disk_gb=dict(type='int'), base_image=dict(type='str'), disk_path=dict(type='str'),
        iso=dict(type='str'), network=dict(type='str'), bridge=dict(type='str'),
        cloud_init=dict(type='dict', options=dict(user=dict(type='str', required=True), password=dict(type='str', no_log=True),
                                                  ssh_keys=dict(type='list', elements='str', no_log=False), hostname=dict(type='str'),
                                                  packages=dict(type='list', elements='str'))),
        autostart=dict(type='bool'), delete_disks=dict(type='bool', default=True), force=dict(type='bool', default=False),
    )
    module = AnsibleModule(argument_spec=args, supports_check_mode=True,
                           mutually_exclusive=[('template', 'base_image', 'disk_path', 'iso'), ('network', 'bridge')])
    p = module.params
    c = Client(module)
    changed, what = False, []
    try:
        d = c.domain(p['name'])
        if p['state'] == 'absent':
            if d is not None:
                changed = True
                if not module.check_mode:
                    c.stop(d['uuid'], force=True)
                    q = '?storage=remove' if p['delete_disks'] else ''
                    c.call('DELETE', '/api/v1/domains/%s%s' % (quote(d['uuid'], safe=''), q))
            module.exit_json(changed=changed)
        if d is None:
            changed = True
            what.append('created')
            if module.check_mode:
                module.exit_json(changed=True, what=what)
            uuid = create(c, p)
            if p.get('autostart'):
                c.call('PUT', '/api/v1/domains/%s/autostart' % quote(uuid, safe=''), dict(enabled=True))
            module.exit_json(changed=True, what=what, machine=c.domain(uuid))
        uuid = d['uuid']
        want_cpu = p.get('vcpus') if p.get('vcpus') and p['vcpus'] != d.get('vcpus') else None
        want_mem = p.get('memory_mb') if p.get('memory_mb') and p['memory_mb'] != (d.get('maxMemoryKiB', 0) >> 10) else None
        if want_cpu or want_mem:
            changed = True
            what.append('resized')
            if not module.check_mode:
                c.resize(uuid, want_cpu or d.get('vcpus'), want_mem or (d.get('maxMemoryKiB', 0) >> 10))
        if p.get('autostart') is not None and p['autostart'] != d.get('autostart'):
            changed = True
            what.append('autostart')
            if not module.check_mode:
                c.call('PUT', '/api/v1/domains/%s/autostart' % quote(uuid, safe=''), dict(enabled=p['autostart']))
        if p['state'] == 'running' and not is_running(d):
            changed = True
            what.append('started')
            if not module.check_mode:
                c.action(uuid, 'start')
        if p['state'] == 'stopped' and is_running(d):
            changed = True
            what.append('stopped')
            if not module.check_mode:
                c.stop(uuid, force=p['force'])
        module.exit_json(changed=changed, what=what, machine=c.domain(uuid))
    except APIError as e:
        module.fail_json(msg=str(e), status=e.status)


if __name__ == '__main__':
    main()
