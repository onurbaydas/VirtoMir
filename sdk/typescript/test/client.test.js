import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { VirtoMirClient, VirtoMirError, isRunning } from '../dist/index.js';

const U = '6f1c2a5e-0000-4000-8000-000000000001';

function serve(handler) {
  return new Promise((resolve) => {
    const seen = [];
    const srv = createServer((req, res) => {
      let body = '';
      req.on('data', (c) => (body += c));
      req.on('end', () => {
        seen.push({ method: req.method, url: req.url, auth: req.headers.authorization, body });
        handler(req, res, body);
      });
    });
    srv.listen(0, '127.0.0.1', () => resolve({ srv, seen, url: `http://127.0.0.1:${srv.address().port}` }));
  });
}

function json(res, status, v) {
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(v === undefined ? '' : JSON.stringify(v));
}

test('lists machines with the token and reads null as empty', async () => {
  let reply = { domains: [{ uuid: U, name: 'web', state: 'running', vcpus: 2, maxMemoryKiB: 2097152, autostart: true }] };
  const { srv, seen, url } = await serve((req, res) => json(res, 200, reply));
  try {
    const c = new VirtoMirClient(url + '/', { token: 'tok' });
    const list = await c.listMachines();
    assert.equal(list.length, 1);
    assert.equal(list[0].name, 'web');
    assert.ok(isRunning(list[0]));
    assert.equal(seen[0].auth, 'Bearer tok');
    assert.equal(seen[0].url, '/api/v1/domains');
    reply = { domains: null };
    assert.deepEqual(await c.listMachines(), []);
    assert.equal((await c.findMachine('nope')), undefined);
  } finally {
    srv.close();
  }
});

test('power, autostart, snapshots and host routing hit the right paths', async () => {
  const { srv, seen, url } = await serve((req, res) => {
    if (req.method === 'POST' && req.url.endsWith('/snapshots')) return json(res, 201, { name: 'before-upgrade' });
    if (req.url.includes('/actions/')) return json(res, 202);
    json(res, 204);
  });
  try {
    const c = new VirtoMirClient(url, { token: 'tok', host: 'hv2' });
    await c.power(U, 'force-off');
    await c.setAutostart(U, false);
    assert.equal(await c.createSnapshot(U, 'before-upgrade', 'x'), 'before-upgrade');
    await c.revertSnapshot(U, 'before-upgrade', false);
    await c.deleteMachine(U, true);
    assert.deepEqual(
      seen.map((s) => `${s.method} ${s.url}`),
      [
        `POST /api/v1/hosts/hv2/domains/${U}/actions/force-off`,
        `PUT /api/v1/hosts/hv2/domains/${U}/autostart`,
        `POST /api/v1/hosts/hv2/domains/${U}/snapshots`,
        `POST /api/v1/hosts/hv2/domains/${U}/snapshots/before-upgrade/revert?safety=false`,
        `DELETE /api/v1/hosts/hv2/domains/${U}?storage=remove`,
      ],
    );
    assert.deepEqual(JSON.parse(seen[1].body), { enabled: false });
  } finally {
    srv.close();
  }
});

test('an error answer becomes a VirtoMirError with the message', async () => {
  const { srv, url } = await serve((req, res) => json(res, 404, { error: 'machine not found' }));
  try {
    const c = new VirtoMirClient(url);
    await assert.rejects(c.getMachine(U), (e) => e instanceof VirtoMirError && e.notFound && /machine not found/.test(e.message));
  } finally {
    srv.close();
  }
});

test('refuses a base URL that is not a URL', () => {
  assert.throws(() => new VirtoMirClient('hv1:8420x y'));
});
