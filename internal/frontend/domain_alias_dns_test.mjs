import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const source = await readFile(new URL('www/js/settings.js', import.meta.url), 'utf8');
const { methods } = vm.runInNewContext(source
  .replace(/^import\s[^;]+;/gm, '')
  .replace('export const settingsMethods', 'globalThis.methods') + '\n;({ methods })');

const system = { mode: 'system', servers: [], effectiveServers: ['127.0.0.1:53'], canManage: true };

test('DNS settings save ordered custom servers and reset to system DNS', async () => {
  const calls = [];
  const state = {
    ...methods,
    api: {
      getDomainAliasDNS: async () => system,
      updateDomainAliasDNS: async ({ mode, servers }) => {
        calls.push({ mode, servers: [...servers] });
        return { ...system, mode, servers };
      },
    },
    showToast() {},
  };
  await state.loadDomainAliasDNS();
  state.domainAliasDNS.mode = 'custom';
  state.domainAliasDNS.servers = '192.0.2.53, 2001:db8::53';
  await state.saveDomainAliasDNS();
  state.domainAliasDNS.mode = 'system';
  await state.saveDomainAliasDNS();
  assert.deepEqual(calls, [
    { mode: 'custom', servers: ['192.0.2.53', '2001:db8::53'] },
    { mode: 'system', servers: [] },
  ]);
});

test('a late save from the previous server does not change the selected server draft', async () => {
  let finish;
  const state = {
    ...methods,
    activeRemoteId: 'remote-a',
    api: {
      getDomainAliasDNS: async () => ({ ...system, canManage: state.activeRemoteId === 'remote-a' }),
      updateDomainAliasDNS: () => new Promise(resolve => { finish = resolve; }),
    },
    showToast() { assert.fail('stale save displayed a success toast'); },
  };
  await state.loadDomainAliasDNS();
  const saving = state.saveDomainAliasDNS();
  state.activeRemoteId = 'remote-b';
  await state.loadDomainAliasDNS();
  finish({ ...system, mode: 'custom', servers: ['192.0.2.53'] });
  await saving;
  assert.equal(state.domainAliasDNS.status.canManage, false);
  assert.equal(state.domainAliasDNS.mode, 'system');
});
