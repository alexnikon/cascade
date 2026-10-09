import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import assert from 'node:assert/strict';
import test from 'node:test';

const utilsSource = await readFile(new URL('www/js/utils.js', import.meta.url), 'utf8');
const { updateTransferRates, sortByProperty, bytes } = await import(`data:text/javascript;base64,${Buffer.from(utilsSource).toString('base64')}`);

test('fractional byte rates retain the B unit', () => {
  assert.equal(bytes(0.2), '0.2 B');
  assert.equal(bytes(2000), '2 KB');
});

function measure(state, rx, tx, time, scope = 'local/wg10') {
  const sample = { transferRx: rx, transferTx: tx };
  updateTransferRates(state, sample, scope, time);
  return [sample.transferRxCurrent, sample.transferTxCurrent];
}

test('rates use elapsed seconds, including delayed polls', () => {
  const state = {};
  assert.deepEqual(measure(state, 100, 200, 0), [0, 0]);
  assert.deepEqual(measure(state, 10100, 20200, 5000), [2000, 4000]);
  assert.deepEqual(measure(state, 20100, 40200, 15000), [1000, 2000]);
  assert.deepEqual(measure(state, 20100, 40200, 20000), [0, 0]);
});

test('counter resets rebase each direction without negative rates', () => {
  const state = {};
  measure(state, 10000, 10000, 0);
  assert.deepEqual(measure(state, 0, 15000, 5000), [0, 1000]);
  assert.deepEqual(measure(state, 5000, 0, 10000), [1000, 0]);
  assert.deepEqual(measure(state, 10000, 5000, 15000), [1000, 1000]);
});

test('zero intervals and missing counters produce finite zero rates', () => {
  const state = {};
  measure(state, undefined, undefined, 1000);
  assert.deepEqual(measure(state, 100, 200, 1000), [0, 0]);
  assert.deepEqual(measure(state, 1100, 2200, 2000), [1000, 2000]);
});

test('servers and interfaces keep independent baselines for matching peer IDs', () => {
  const state = {};
  measure(state, 100, 200, 0, 'local/wg10');
  assert.deepEqual(measure(state, 90000, 90000, 5000, 'remote/wg10'), [0, 0]);
  assert.deepEqual(measure(state, 90000, 90000, 5000, 'local/wg11'), [0, 0]);
  assert.deepEqual(measure(state, 10100, 20200, 10000, 'local/wg10'), [1000, 2000]);
});

const paths = [
  { file: 'clients.js', exported: 'clientMethods', method: 'refresh', api: 'getClients', output: 'clients', cache: 'clientsPersist', list: true },
  { file: 'poller.js', exported: 'pollerMethods', method: '_refreshPeersNow', api: 'getTunnelInterfacePeers', output: 'selectedInterfacePeers', cache: 'peersPersist' },
  { file: 'poller.js', exported: 'pollerMethods', method: '_refreshAllPeersNow', api: 'getAllTunnelPeers', output: 'allPeers', cache: 'peersPersist' },
];

for (const path of paths) {
  const source = await readFile(new URL(`www/js/${path.file}`, import.meta.url), 'utf8');
  let now = 0;
  const sandbox = { updateTransferRates, sortByProperty, performance: { now: () => now }, console };
  vm.runInNewContext(source.replace(/^import .*;\n/gm, '').replace(`export const ${path.exported}`, `globalThis.${path.exported}`), sandbox);
  const refresh = sandbox[path.exported][path.method];
  const response = (rx, tx) => {
    const peers = [{ id: 'peer-1', name: 'Client', interfaceId: 'wg10', transferRx: rx, transferTx: tx }];
    return path.list ? peers : { peers };
  };
  const context = () => ({
    authenticated: true, activeInterfaceId: 'wg10', activeRemoteId: null,
    avatarSettings: {}, peersPersist: {}, clientsPersist: {},
    api: { [path.api]: async () => response(0, 0) },
  });

  test(`${path.method} displays bytes per second and retains hover state`, async () => {
    const app = context();
    app[path.cache]['peer-1'] = { hoverTx: true, hoverRx: false };
    now = 0;
    await refresh.call(app);
    assert.equal(app[path.output][0].transferTxCurrent, 0);
    app.api[path.api] = async () => response(10000, 20000);
    now = 5000;
    await refresh.call(app);
    assert.equal(app[path.output][0].transferRxCurrent, 2000);
    assert.equal(app[path.output][0].transferTxCurrent, 4000);
    assert.equal(app[path.output][0].hoverTx, true);
  });

  test(`${path.method} discards superseded responses before changing baselines`, async () => {
    const app = context();
    now = 0;
    await refresh.call(app);
    let resolveOld;
    app.api[path.api] = () => new Promise(resolve => { resolveOld = resolve; });
    const oldRequest = refresh.call(app);
    app.api[path.api] = async () => response(10000, 20000);
    now = 5000;
    await refresh.call(app);
    resolveOld(response(90000, 90000));
    await oldRequest;
    app.api[path.api] = async () => response(20000, 40000);
    now = 10000;
    await refresh.call(app);
    assert.equal(app[path.output][0].transferTxCurrent, 4000);
    assert.equal(app[path.output][0].transferRxCurrent, 2000);
  });

  test(`${path.method} rejects responses from a server that is no longer selected`, async () => {
    const app = context();
    let resolveOld;
    app.api[path.api] = () => new Promise(resolve => { resolveOld = resolve; });
    const oldRequest = refresh.call(app);
    app.activeRemoteId = 'remote-1';
    resolveOld(response(90000, 90000));
    await oldRequest;
    assert.equal(Object.keys(app[path.cache]).length, 0);
    assert.equal(app[path.output], undefined);
  });
}
