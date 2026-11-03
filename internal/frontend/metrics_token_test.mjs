import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { webcrypto } from 'node:crypto';
import vm from 'node:vm';
import test from 'node:test';

const source = await readFile(new URL('www/js/settings.js', import.meta.url), 'utf8');
function fixture() {
  const copied = [];
  const context = { crypto: webcrypto, navigator: { clipboard: { async writeText(value) { copied.push(value); } } } };
  vm.createContext(context);
  vm.runInContext(source.replace(/^import\s[^;]+;/gm, '').replace('export const settingsMethods', 'globalThis.methods'), context);
  return { state: { ...context.methods, metricsSettings: { canManage: true, enabled: true, tokenConfigured: true, token: '', clearToken: true }, showToast() {} }, copied };
}

test('generation stages a fresh token and copying uses the staged value', async () => {
  const { state, copied } = fixture();
  state.generateMetricsToken();
  const first = state.metricsSettings.token;
  assert.match(first, /^[0-9a-f]{64}$/);
  assert.equal(state.metricsSettings.clearToken, false);
  assert.equal(state.metricsSettings.tokenConfigured, true);
  await state.copyMetricsToken();
  assert.deepEqual(copied, [first]);
  state.generateMetricsToken();
  assert.notEqual(state.metricsSettings.token, first);
  state.metricsSettings.token = '';
  await state.copyMetricsToken();
  assert.equal(copied.length, 1);
});

test('read-only and disabled endpoints cannot generate or copy', async () => {
  for (const flag of ['canManage', 'enabled']) {
    const { state, copied } = fixture();
    state.metricsSettings[flag] = false;
    state.metricsSettings.token = 'unchanged';
    state.generateMetricsToken();
    await state.copyMetricsToken();
    assert.equal(state.metricsSettings.token, 'unchanged');
    assert.deepEqual(copied, []);
  }
});
