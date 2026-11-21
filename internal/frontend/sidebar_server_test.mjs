import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const source = await readFile(new URL('www/js/settings.js', import.meta.url), 'utf8');

test('sidebar metadata ignores stale settings after switching local to remote and back', async () => {
  const pending = [];
  const context = vm.createContext({ console });
  vm.runInContext(source.replace(/^import\s[^;]+;/gm, '')
    .replace('export const settingsMethods', 'globalThis.methods'), context);
  const app = {
    ...context.methods,
    activeRemoteId: null,
    api: {
      getSettings: () => new Promise(resolve => pending.push(resolve)),
      getTemplates: async () => ({ templates: [] }),
    },
    loadWebAccess: async () => {},
    loadAliases: async () => {},
  };

  const oldLocal = app.loadSettings();
  app.activeRemoteId = 'remote';
  const remote = app.loadSettings();
  app.activeRemoteId = null;
  const newLocal = app.loadSettings();

  pending[2]({ routerName: 'Local', resolvedPublicIP: '192.0.2.10' });
  await newLocal;
  pending[1]({ routerName: 'Remote', resolvedPublicIP: '192.0.2.20' });
  pending[0]({ routerName: 'Old local', resolvedPublicIP: '192.0.2.30' });
  await Promise.all([remote, oldLocal]);

  assert.equal(app.settingsServerKey, '__local__');
  assert.equal(app.localServerName, 'Local');
  assert.equal(app.globalSettings.resolvedPublicIP, '192.0.2.10');
});
