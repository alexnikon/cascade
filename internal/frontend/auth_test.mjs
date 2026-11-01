import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import test from 'node:test';

const Vue = createRequire(import.meta.url)('./www/js/vendor/vue.min.js');
const source = await readFile(new URL('www/js/auth.js', import.meta.url), 'utf8');
const { authMethods } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
const tick = () => new Promise(resolve => Vue.nextTick(resolve));

function fixture() {
  const focus = [];
  const messages = [];
  let blurred = 0;
  globalThis.document = {
    activeElement: { id: 'totp-code', blur() { blurred++; } },
    getElementById: () => null,
  };
  const app = new Vue({
    data: {
      authenticated: false, totpRequired: true, totpCode: '', lastAutoTOTP: '',
      authenticating: false, username: 'owner', password: 'secret', remember: false,
    },
    methods: {
      ...authMethods,
      _loginRequestWithDelay: request => request(),
      showToast: message => messages.push(message),
      refresh: async () => {}, loadTunnelInterfaces: async () => {}, refreshAllPeers() {},
      loadSettings() {}, loadClientGroups() {}, loadUsers() {}, loadCurrentUser() {},
      loadRemotes() {}, loadDashboard() {}, metricsStartPoller() {},
    },
    watch: {
      authenticated(value) { if (value === false) this.focusLoginField(); },
      totpRequired() { this.focusLoginField(); },
    },
  });
  app.$refs.loginUsername = { focus: () => focus.push('username') };
  app.$refs.loginTOTP = { focus: () => focus.push('totp') };
  app.api = { getSession: async () => ({ authenticated: true, requiresPassword: true }) };
  return { app, focus, messages, blurred: () => blurred };
}

test('TOTP success never focuses username while session confirmation is pending', async () => {
  const f = fixture();
  f.app.totpCode = '123456';
  f.app.api.verifyTOTP = async () => {};
  let confirm;
  f.app.api.getSession = () => new Promise(resolve => { confirm = resolve; });
  const pending = f.app.loginStep2();
  await tick();
  assert.equal(f.app.totpRequired, true);
  assert.equal(f.app.authenticated, false);
  assert.deepEqual(f.focus, []);
  confirm({ authenticated: true, requiresPassword: true });
  await pending;
  await tick();
  assert.equal(f.app.authenticated, true);
  assert.equal(f.app.totpRequired, false);
  assert.equal(f.app.totpCode, '');
  assert.deepEqual(f.focus, []);
  assert.equal(f.blurred(), 1);
});

test('complete input submits once, failed unchanged input does not loop, manual retry works', async () => {
  const f = fixture();
  let calls = 0;
  f.app.api.verifyTOTP = async () => { calls++; throw new Error('Invalid code'); };
  const input = value => f.app.onTOTPInput({ target: { value } });
  input('12345');
  assert.equal(calls, 0);
  input('123456');
  input('123456');
  await tick();
  assert.equal(calls, 1);
  assert.equal(f.app.totpRequired, true);
  assert.equal(f.app.totpCode, '123456');
  assert.deepEqual(f.messages, ['Invalid code']);
  input('123456');
  await tick();
  assert.equal(calls, 1);
  await f.app.loginStep2();
  assert.equal(calls, 2);
  input('123457');
  await tick();
  assert.equal(calls, 3);
  input('12345');
  input('123457');
  await tick();
  assert.equal(calls, 4);
});

test('failed session confirmation keeps TOTP visible without focusing username', async () => {
  const f = fixture();
  f.app.totpCode = '123456';
  f.app.api.verifyTOTP = async () => {};
  f.app.api.getSession = async () => { throw new Error('Offline'); };
  await f.app.loginStep2();
  await tick();
  assert.equal(f.app.authenticated, false);
  assert.equal(f.app.totpRequired, true);
  assert.equal(f.app.totpCode, '123456');
  assert.deepEqual(f.focus, []);
});

test('stale scheduled focus cannot cross auth steps or restore focus after login', async () => {
  const f = fixture();
  f.app.focusLoginField();
  f.app.authenticated = true;
  await tick();
  assert.deepEqual(f.focus, []);
  f.app.authenticated = false;
  f.app.focusLoginField();
  f.app.backToLogin();
  await tick();
  assert.deepEqual(f.focus, ['username']);
  assert.equal(f.app.totpCode, '');
});

test('password login keeps autofill DOM values and supports both auth paths', async () => {
  for (const totp of [true, false]) {
    const f = fixture();
    f.app.totpRequired = false;
    await tick();
    f.focus.length = 0;
    globalThis.document.getElementById = id => ({ value: id === 'login-username' ? ' autofilled ' : 'password' });
    f.app.api.createSession = async credentials => {
      assert.equal(credentials.username, 'autofilled');
      assert.equal(credentials.password, 'password');
      return { totp_required: totp };
    };
    await f.app.login();
    await tick();
    assert.equal(f.app.authenticated, !totp);
    assert.equal(f.app.totpRequired, totp);
    assert.equal(f.app.password, '');
    assert.deepEqual(f.focus, totp ? ['totp'] : []);
  }
});

test('background version load updates the sidebar state without a toast', async () => {
  const source = await readFile(new URL('www/js/interfaces.js', import.meta.url), 'utf8');
  const { interfaceMethods } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
  const originalFetch = globalThis.fetch;
  const messages = [];
  const version = { updateAvailable: true, latestVersion: 'v99.0.0' };
  globalThis.fetch = async () => ({ ok: true, json: async () => version });
  try {
    const app = { versionInfo: null, showToast: message => messages.push(message) };
    await interfaceMethods.loadVersionInfo.call(app);
    assert.equal(app.versionInfo, version);
    assert.deepEqual(messages, []);
    await interfaceMethods.checkForUpdates.call(app);
    assert.deepEqual(messages, ['Update available: v99.0.0']);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
