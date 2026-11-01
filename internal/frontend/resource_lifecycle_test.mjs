import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import test from 'node:test';

const source = await readFile(new URL('www/js/aliases.js', import.meta.url), 'utf8');
function fixture() {
  const timers = new Map(); let id = 0;
  const sandbox = { console, setTimeout(fn) { timers.set(++id, fn); return id; }, clearTimeout(id) { timers.delete(id); } };
  vm.createContext(sandbox);
  vm.runInContext(source.replace(/import[^;]+;/g, '').replace('export const aliasMethods', 'globalThis.methods'), sandbox);
  const state = { ...sandbox.methods, api: {}, loadAliases: async () => {}, showToast() {} };
  return { state, timers, async tick() { const [key, fn] = timers.entries().next().value; timers.delete(key); await fn(); } };
}

test('alias polling waits for the previous response and stops for unknown jobs', async () => {
  const f = fixture(); let resolve;
  f.state.api.getAliasJobStatus = () => new Promise(r => { resolve = r; });
  f.state._pollAliasJob('alias', 'job');
  const pending = f.tick();
  assert.equal(f.timers.size, 0);
  resolve({ status: 'running' }); await pending;
  assert.equal(f.timers.size, 1);
  f.state.api.getAliasJobStatus = async () => ({ status: 'unknown' });
  await f.tick(); assert.equal(f.timers.size, 0); assert.equal(f.state.aliasGeneratingId, null);
});

test('cancelled alias polling ignores its in-flight response', async () => {
  const f = fixture(); let resolve;
  f.state.api.getAliasJobStatus = () => new Promise(r => { resolve = r; });
  f.state._pollAliasJob('alias', 'job'); const pending = f.tick();
  f.state._stopAliasJobPoll(); resolve({ status: 'running' }); await pending;
  assert.equal(f.timers.size, 0); assert.equal(f.state.aliasGeneratingId, null);
});

test('updated browser libraries retain their globals and fixtures', async () => {
  const sandbox = {}; vm.createContext(sandbox);
  for (const file of ['marked.min.js', 'sha256.min.js', 'timeago.full.min.js']) {
    vm.runInContext(await readFile(new URL(`www/js/vendor/${file}`, import.meta.url), 'utf8'), sandbox);
  }
  assert.equal(sandbox.sha256('abc'), 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
  assert.match(sandbox.marked.parse('# Heading'), /<h1>Heading<\/h1>/);
  assert.equal(typeof sandbox.timeago.format, 'function');
});
