import { readFile } from 'node:fs/promises';
import assert from 'node:assert/strict';
import test from 'node:test';

async function methods(file, name) {
  const source = await readFile(new URL(`www/js/${file}`, import.meta.url), 'utf8');
  return (await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`))[name];
}
const wizard = await methods('wizards.js', 'wizardMethods');
const gateways = await methods('gateways.js', 'gatewayMethods');

function setup(failPath = '') {
  const calls = [];
  const context = {
    ...wizard,
    wizardS2S: { remoteId: 'remote', selectedIfaceIds: ['wg11'], createSrcAlias: false, dstType: 'all', protocol: 'wireguard', localIfaceName: 'local', remoteIfaceName: 'remote', srcAliasName: 'clients', gatewayName: 'gateway', fwRuleName: 'pbr', fallback: 'drop' },
    tunnelInterfaces: [{ id: 'wg11', address: '10.3.0.1/24' }],
    $set: (array, index, value) => { array[index] = value; },
    loadTunnelInterfaces: async () => {},
    api: {
      createTunnelInterface: async () => ({ id: 'wg13' }),
      startTunnelInterface: async () => {},
      call: async call => { calls.push(call); return call.path === '/diagnostics/ping' ? { reachable: true } : {}; },
      createGateway: async body => { calls.push({ path: 'gateway', body }); return { id: 'gw' }; },
      createFirewallRule: async () => ({ id: 'rule' }),
      applyFirewallRules: async () => {},
      remoteCall: async call => {
        calls.push(call);
        if (call.method === 'post' && call.path === failPath) throw new Error('Injected failure');
        if (call.path === '/system/interfaces') return { interfaces: [{ name: 'eth0' }] };
        if (call.path === '/aliases') return { id: 'remote-alias' };
        if (call.path === '/tunnel-interfaces') return call.method === 'get' ? { interfaces: [] } : { id: 'wg10' };
        return {};
      },
    },
  };
  return { context, calls };
}

test('S2S monitors peer and scopes remote NAT even without local alias', async () => {
  const { context, calls } = setup();
  await context.wizardS2SApply();
  assert.equal(context.wizardS2S.done, true);
  const gateway = calls.find(c => c.path === 'gateway').body;
  assert.equal(gateway.monitorAddress, gateway.gatewayIP);
  assert.deepEqual(calls.find(c => c.path === '/aliases').body.entries, ['10.3.0.0/24']);
  assert.equal(calls.find(c => c.path === '/nat/rules' && c.method === 'post').body.sourceAliasId, 'remote-alias');
});
for (const path of ['/nat/rules', '/routing/routes']) {
  test(`S2S reports partial configuration when ${path} fails`, async () => {
    const { context } = setup(path);
    await context.wizardS2SApply();
    assert.equal(context.wizardS2S.done, false);
    assert.match(context.wizardS2S.fatalError, /Injected failure/);
  });
}
test('admin toggle never sends stale monitor fields', async () => {
  let payload;
  const gw = { id: 'gw', adminDown: false, monitorAddress: '' };
  await gateways.toggleGatewayAdminDown.call({ gateways: [gw], api: { updateGateway: async body => { payload = body; } } }, gw);
  assert.deepEqual(payload, { gatewayId: 'gw', adminDown: true });
});

test('internet monitor creates narrow transit NAT and probes the chosen target through S2S', async () => {
  const { context, calls } = setup();
  Object.assign(context.wizardS2S, { monitorMode: 'internet', monitorIP: '1.1.1.1', remoteEgress: 'eth0' });
  await context.wizardS2SApply();
  assert.equal(context.wizardS2S.done, true);
  assert.equal(calls.find(c => c.path === 'gateway').body.monitorAddress, '1.1.1.1');
  const nat = calls.filter(c => c.path === '/nat/rules' && c.method === 'post');
  assert.equal(nat.length, 2);
  assert.equal(nat[0].body.sourceAliasId, 'remote-alias');
  assert.equal(nat[1].body.source, '10.255.255.1/32');
  assert.equal(nat[1].body.outInterface, 'eth0');
  assert.deepEqual(calls.find(c => c.path === '/diagnostics/ping').body, { host: '1.1.1.1', interface: 'wg13', count: 3 });
});

test('internet monitor failure never reports completed setup', async () => {
  const { context } = setup();
  Object.assign(context.wizardS2S, { monitorMode: 'internet', monitorIP: '1.1.1.1', remoteEgress: 'eth0' });
  context.api.call = async () => ({ reachable: false });
  await context.wizardS2SApply();
  assert.equal(context.wizardS2S.done, false);
  assert.match(context.wizardS2S.fatalError, /Monitor IP did not reply/);
});

test('transit NAT failure prevents probing and success', async () => {
  const { context, calls } = setup();
  Object.assign(context.wizardS2S, { monitorMode: 'internet', monitorIP: '1.1.1.1', remoteEgress: 'eth0' });
  const original = context.api.remoteCall;
  context.api.remoteCall = async call => {
    if (call.path === '/nat/rules' && call.body?.source) throw new Error('Transit NAT failed');
    return original(call);
  };
  await context.wizardS2SApply();
  assert.equal(context.wizardS2S.done, false);
  assert.match(context.wizardS2S.fatalError, /Transit NAT failed/);
  assert.equal(calls.some(c => c.path === '/diagnostics/ping'), false);
});

test('gateway forms explain custom tunnel targets without blocking uplinks', async () => {
  const template = await readFile(new URL('templates/views/gateways.html', import.meta.url), 'utf8');
  for (const form of ['gatewayCreate', 'gatewayEdit']) {
    const condition = template.match(new RegExp(`v-if="(${form}\\.monitorAddress[^"\\n]+)" class="text-xs text-amber`))[1];
    const visible = new Function(form, `return Boolean(${condition})`);
    assert.equal(visible({ interface: 'wg13', gatewayIP: '10.255.255.6', monitorAddress: '1.1.1.1' }), true);
    assert.equal(visible({ interface: 'wg13', gatewayIP: '10.255.255.6', monitorAddress: '10.255.255.6' }), false);
    assert.equal(visible({ interface: 'ens1', gatewayIP: '192.0.2.1', monitorAddress: '1.1.1.1' }), false);
  }
});
