# @onurbaydas/virtomir

A small client for the [VirtoMir](https://github.com/onurbaydas/VirtoMir) API (`/api/v1`): machines, power, autostart and snapshots, with a `request()` for everything else.

## Install

The package is on GitHub Packages. Tell npm where the `@onurbaydas` scope lives (a GitHub token with `read:packages` is needed, even for public packages):

```
echo "@onurbaydas:registry=https://npm.pkg.github.com" >> .npmrc
npm install @onurbaydas/virtomir
```

## Use

Make an API token on the **Account** page of VirtoMir.

```ts
import { VirtoMirClient } from '@onurbaydas/virtomir';

const vm = new VirtoMirClient('https://hv1:8420', { token: process.env.VIRTOMIR_TOKEN });
for (const m of await vm.listMachines()) console.log(m.name, m.state);

const web = await vm.findMachine('web');
if (web) {
  await vm.createSnapshot(web.uuid, 'before-upgrade');
  await vm.power(web.uuid, 'reboot');
}

// Any other endpoint:
const pools = await vm.request('GET', '/api/v1/pools');
```

`{ host: 'hv2' }` sends every call to a cluster member through this host. A self-signed host certificate is trusted with `NODE_EXTRA_CA_CERTS=/path/to/cert.pem`. Errors are `VirtoMirError` with `status` and the API's message.
