import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import test from 'node:test';

const settingsSource = await readFile(new URL('www/js/settings.js', import.meta.url), 'utf8');
const authSource = await readFile(new URL('www/js/auth.js', import.meta.url), 'utf8');
const apiSource = await readFile(new URL('www/js/api.js', import.meta.url), 'utf8');
const { API } = await import(`data:text/javascript;base64,${Buffer.from(apiSource).toString('base64')}`);
const { authMethods } = await import(`data:text/javascript;base64,${Buffer.from(authSource).toString('base64')}`);

function fixture() {
  const confirmations = [];
  const messages = [];
  let confirmResult = true;
  const context = { window: { confirm(message) { confirmations.push(message); return confirmResult; } } };
  vm.createContext(context);
  vm.runInContext(settingsSource.replace(/^import\s[^;]+;/gm, '').replace('export const settingsMethods', 'globalThis.methods'), context);
  const state = {
    ...authMethods, ...context.methods,
    authenticated: true, activeRemoteId: null, activeSessions: [],
    activeSessionsError: '', activeSessionsLoading: false, revokingSessionID: null,
    api: { clearRemote() {}, getSessions: async () => ({ sessions: [{ id: 'current' }] }) },
    showToast: (message) => messages.push(message),
  };
  return { state, confirmations, messages, cancel() { confirmResult = false; } };
}

const session = { id: 'one', username: 'alice', device: 'Mac', browser: 'Safari', current: false };

test('sessions load, display errors, and discard stale responses after a server switch', async () => {
  const { state } = fixture();
  await state.loadActiveSessions();
  assert.equal(state.activeSessions[0].id, 'current');
  assert.equal(state.activeSessionsLoading, false);
  state.api.getSessions = async () => { throw new Error('Offline'); };
  await state.loadActiveSessions();
  assert.equal(state.activeSessions.length, 0);
  assert.equal(state.activeSessionsError, 'Offline');
  let finish;
  state.api.getSessions = () => new Promise(resolve => { finish = resolve; });
  const pending = state.loadActiveSessions();
  state.activeRemoteId = 'remote';
  finish({ sessions: [session] });
  await pending;
  assert.equal(state.activeSessions.length, 0);
});

test('only the latest list request controls the displayed state', async () => {
  const { state } = fixture();
  const requests = [];
  state.api.getSessions = () => new Promise(resolve => requests.push(resolve));
  const first = state.loadActiveSessions();
  const second = state.loadActiveSessions();
  requests[1]({ sessions: [{ id: 'latest' }] });
  await second;
  requests[0]({ sessions: [session] });
  await first;
  assert.equal(state.activeSessions[0].id, 'latest');
});

test('revocation requires confirmation identifying the user and device', async () => {
  const f = fixture();
  f.cancel();
  f.state.api.revokeSession = () => { assert.fail('cancelled revocation sent a request'); };
  await f.state.revokeActiveSession(session);
  assert.match(f.confirmations[0], /alice.*Mac.*Safari/);
});

test('revoking another session refreshes the list; failures retain authentication', async () => {
  const f = fixture();
  f.state.api.revokeSession = async id => { assert.equal(id, session.id); return { current: false }; };
  await f.state.revokeActiveSession(session);
  assert.equal(f.state.authenticated, true);
  assert.equal(f.state.activeSessions[0].id, 'current');
  assert.equal(f.state.revokingSessionID, null);
  f.state.api.revokeSession = async () => { throw new Error('Revoke failed'); };
  await f.state.revokeActiveSession(session);
  assert.equal(f.state.authenticated, true);
  assert.equal(f.messages.at(-1), 'Revoke failed');
});

test('current local revocation clears auth and rejects in-flight list results', async () => {
  const { state, confirmations } = fixture();
  let finish;
  state.api.getSessions = () => new Promise(resolve => { finish = resolve; });
  const loading = state.loadActiveSessions();
  state.api.revokeSession = async () => ({ current: true });
  await state.revokeActiveSession({ ...session, current: true });
  assert.equal(state.authenticated, false);
  assert.equal(state.activeRemoteId, null);
  assert.match(confirmations[0], /signed out/);
  finish({ sessions: [session] });
  await loading;
  assert.equal(state.activeSessions.length, 0);
  assert.equal(state.activeSessionsLoading, false);
});

test('remote revocation and responses from a previous server never sign out locally', async () => {
  const { state } = fixture();
  state.activeRemoteId = 'remote';
  state.api.revokeSession = async () => ({ current: true });
  await state.revokeActiveSession(session);
  assert.equal(state.authenticated, true);
  let finish;
  state.api.revokeSession = () => new Promise(resolve => { finish = resolve; });
  const pending = state.revokeActiveSession(session);
  state.activeRemoteId = null;
  finish({ current: true });
  await pending;
  assert.equal(state.authenticated, true);
});

function response(status, expired = false) {
  return new Response(JSON.stringify(status >= 400 ? { error: 'Unauthorized' } : { authenticated: true }), {
    status,
    headers: expired ? { 'X-Cascade-Session-Expired': '1' } : {},
  });
}

test('API preserves local authentication calls and concurrent remote routing', async () => {
  globalThis.window = { location: { pathname: '/hidden/', origin: 'http://cascade.test' } };
  const originalFetch = globalThis.fetch;
  const paths = [];
  const requests = [];
  globalThis.fetch = url => { paths.push(url); return new Promise(resolve => requests.push(resolve)); };
  try {
    const api = new API();
    api.setRemote('remote');
    const local = api.getSession();
    const remote = api.getSessions();
    assert.equal(api._remoteId, 'remote');
    assert.match(paths[0], /\/hidden\/api\/session$/);
    assert.match(paths[1], /\/hidden\/api\/remotes\/remote\/proxy\/sessions$/);
    requests[0](response(200)); requests[1](response(200));
    await Promise.all([local, remote]);
    const logout = api.deleteSession();
    assert.match(paths[2], /\/hidden\/api\/session$/);
    requests[2](response(200));
    await logout;
  } finally { globalThis.fetch = originalFetch; }
});

test('API distinguishes revoked local sessions, remote authentication failures, and invalid credentials', async () => {
  globalThis.window = { location: { pathname: '/', origin: 'http://cascade.test' } };
  const originalFetch = globalThis.fetch;
  try {
    const api = new API();
    let expired = 0;
    let remoteErrors = 0;
    api._onUnauthorized = () => { expired++; };
    api._onRemoteError = () => { remoteErrors++; };
    globalThis.fetch = async () => response(401);
    await assert.rejects(api.createSession({ username: 'alice', password: 'wrong' }));
    assert.equal(expired, 0);
    api.setRemote('remote');
    await assert.rejects(api.getSessions());
    assert.equal(expired, 0);
    assert.equal(remoteErrors, 1);
    globalThis.fetch = async url => url.endsWith('/api/session')
      ? Response.json({ authenticated: false }) : response(401, true);
    await assert.rejects(api.getSessions());
    assert.equal(expired, 1);
    assert.equal(remoteErrors, 1);
  } finally { globalThis.fetch = originalFetch; }
});

test('a delayed expiry response cannot clear a newer browser authentication', async () => {
  globalThis.window = { location: { pathname: '/', origin: 'http://cascade.test' } };
  const originalFetch = globalThis.fetch;
  try {
    let finish;
    globalThis.fetch = () => new Promise(resolve => { finish = resolve; });
    const api = new API();
    let expired = 0;
    api._onUnauthorized = () => { expired++; };
    const request = api.getSessions();
    api._authGeneration++;
    finish(response(401, true));
    await assert.rejects(request);
    assert.equal(expired, 0);
  } finally { globalThis.fetch = originalFetch; }
});

test('peer config downloads still route to the selected server', async () => {
  globalThis.window = { location: { pathname: '/', origin: 'http://cascade.test' } };
  const originalFetch = globalThis.fetch;
  const paths = [];
  globalThis.fetch = async url => { paths.push(url); return new Response('[Interface]\nPrivateKey = sample'); };
  try {
    const api = new API();
    assert.match(await api.getPeerConfig({ interfaceId: 'wg0', peerId: 'peer' }), /\[Interface\]/);
    api.setRemote('remote');
    await api.getPeerConfig({ interfaceId: 'wg0', peerId: 'peer' });
    assert.equal(paths[0], 'http://cascade.test/api/tunnel-interfaces/wg0/peers/peer/config');
    assert.equal(paths[1], 'http://cascade.test/api/remotes/remote/proxy/tunnel-interfaces/wg0/peers/peer/config');
  } finally { globalThis.fetch = originalFetch; }
});

test('revoked authentication is handled on downloads as well as JSON requests', async () => {
  globalThis.window = { location: { pathname: '/', origin: 'http://cascade.test' } };
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async url => url.endsWith('/api/session')
      ? Response.json({ authenticated: false }) : response(401, true);
  try {
    const api = new API();
    let expired = 0;
    api._onUnauthorized = () => { expired++; };
    await assert.rejects(api.getPeerConfig({ interfaceId: 'wg0', peerId: 'peer' }));
    assert.equal(expired, 1);
  } finally { globalThis.fetch = originalFetch; }
});


test('transient local 401s share a session check and keep a live session', async () => {
  globalThis.window = { location: { pathname: '/hidden/', origin: 'https://cascade.test' } };
  const originalFetch = globalThis.fetch;
  const api = new API();
  api.setRemote('remote');
  let checks = 0;
  let finish;
  let expired = 0;
  api._onUnauthorized = () => { expired++; };
  try {
    globalThis.fetch = async (url, options) => {
      if (!url.endsWith('/api/session')) return response(401, true);
      assert.equal(url, 'https://cascade.test/hidden/api/session');
      assert.equal(options.cache, 'no-store');
      checks++;
      return new Promise(resolve => { finish = resolve; });
    };
    const pending = Promise.allSettled([api.getSessions(), api.getSessions()]);
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(checks, 1);
    finish(Response.json({ authenticated: true }));
    assert.ok((await pending).every(result => result.status === 'rejected'));
    assert.equal(expired, 0);
  } finally { globalThis.fetch = originalFetch; }
});

test('failed and stale session checks cannot clear authentication', async () => {
  globalThis.window = { location: { pathname: '/', origin: 'http://cascade.test' } };
  const originalFetch = globalThis.fetch;
  const api = new API();
  let expired = 0;
  api._onUnauthorized = () => { expired++; };
  try {
    globalThis.fetch = async url => {
      if (url.endsWith('/api/session')) throw new Error('offline');
      return response(401, true);
    };
    await assert.rejects(api.getSessions(), /Unauthorized/);
    let finish;
    globalThis.fetch = async url => url.endsWith('/api/session')
      ? new Promise(resolve => { finish = resolve; }) : response(401, true);
    const pending = assert.rejects(api.getSessions(), /Unauthorized/);
    await new Promise(resolve => setImmediate(resolve));
    api._authGeneration++;
    finish(Response.json({ authenticated: false }));
    await pending;
    assert.equal(expired, 0);
  } finally { globalThis.fetch = originalFetch; }
});

test('page reads recover once after transient authentication and server errors', async () => {
  globalThis.window = { location: { pathname: '/', origin: 'http://cascade.test' } };
  const originalFetch = globalThis.fetch;
  const api = new API();
  try {
    for (const first of [() => response(401, true), () => response(503), () => { throw Error('offline'); }]) {
      let requests = 0;
      globalThis.fetch = async url => {
        if (url.endsWith('/api/session')) return Response.json({ authenticated: true });
        return ++requests === 1 ? first() : Response.json({ widgets: [] });
      };
      assert.deepEqual(await api.getDashboardWidgets(), { widgets: [] });
      assert.equal(requests, 2);
    }
    let writes = 0;
    globalThis.fetch = async () => { writes++; return response(503); };
    await assert.rejects(api.putDashboardWidgets([]));
    assert.equal(writes, 1);
  } finally { globalThis.fetch = originalFetch; }
});

test('timeout aborts a stalled response body and frees the next read', async () => {
  globalThis.window = { location: { pathname: '/', origin: 'http://cascade.test' } };
  const originalFetch = globalThis.fetch;
  const api = new API();
  const withDeadline = api._withDeadline.bind(api);
  api._withDeadline = (_ms, run) => withDeadline(20, run);
  let requests = 0;
  try {
    globalThis.fetch = async (_url, { signal }) => {
      if (++requests === 2) return Response.json({ widgets: [] });
      return {
        status: 200,
        json: () => new Promise((_, reject) => signal.addEventListener('abort', () => reject(signal.reason))),
      };
    };
    assert.deepEqual(await api.getDashboardWidgets(), { widgets: [] });
    assert.equal(requests, 2);
  } finally { globalThis.fetch = originalFetch; }
});

test('an old remote failure cannot disconnect a newer selection of the same server', async () => {
  globalThis.window = { location: { pathname: '/', origin: 'http://cascade.test' } };
  const originalFetch = globalThis.fetch;
  try {
    const api = new API();
    let remoteErrors = 0;
    api._onRemoteError = () => { remoteErrors++; };
    let finish;
    globalThis.fetch = () => new Promise(resolve => { finish = resolve; });
    api.setRemote('b');
    const pending = api.getDashboardWidgets();
    api.clearRemote();
    api.setRemote('b');
    finish(response(502));
    await assert.rejects(pending);
    assert.equal(remoteErrors, 0);

    globalThis.fetch = async () => response(502);
    await assert.rejects(api.getDashboardWidgets());
    assert.equal(remoteErrors, 1);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
