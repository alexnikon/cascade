import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import vm from 'node:vm';
import assert from 'node:assert/strict';
import test from 'node:test';

const require = createRequire(import.meta.url);
const Vue = require('./www/js/vendor/vue.min.js');
const source = await readFile(new URL('www/js/app.js', import.meta.url), 'utf8');
const navigationSource = await readFile(new URL('www/js/navigation.js', import.meta.url), 'utf8');
const { navigationMethods } = await import(`data:text/javascript;base64,${Buffer.from(navigationSource).toString('base64')}`);
let config;
const context = {
  Vue: function (options) { config = options; },
  VueApexCharts: {}, localStorage: {},
  window: { matchMedia: () => ({ matches: false }) },
};
for (const match of source.matchAll(/^import \{([^}]+)\}/gm)) {
  for (const name of match[1].split(',').map(name => name.trim())) context[name] = {};
}
vm.runInNewContext(source.replace(/^import .*;\n/gm, ''), context);

test('system appearance invalidates cached Vue styles without a reload', async () => {
  const applied = [];
  const app = new Vue({
    data: { uiTheme: 'auto', systemDark: false },
    computed: { theme: config.computed.theme },
    methods: { ...navigationMethods, setTheme: theme => applied.push(theme) },
  });
  const observed = [];
  app.$watch('theme', value => observed.push(value));
  assert.equal(app.theme, 'light');
  app.handlePrefersChange({ matches: true });
  await Vue.nextTick();
  assert.equal(app.theme, 'dark');
  app.handlePrefersChange({ matches: false });
  await Vue.nextTick();
  assert.equal(app.theme, 'light');
  assert.deepEqual(observed, ['dark', 'light']);
  assert.deepEqual(applied, ['auto', 'auto']);
  for (const manual of ['light', 'dark']) {
    app.uiTheme = manual;
    app.handlePrefersChange({ matches: manual === 'light' });
    await Vue.nextTick();
    assert.equal(app.theme, manual);
  }
  assert.deepEqual(applied, ['auto', 'auto']);
  app.$destroy();
});

const { interfaceMethods } = await import(`data:text/javascript;base64,${Buffer.from(await readFile(new URL('www/js/interfaces.js', import.meta.url), 'utf8')).toString('base64')}`);
const { aliasMethods } = await import(`data:text/javascript;base64,${Buffer.from(await readFile(new URL('www/js/aliases.js', import.meta.url), 'utf8')).toString('base64')}`);

test('Amnezia panels reset on reopening without clearing edited parameters', async () => {
  const app = new Vue({
    data: {
      showInterfaceCreate: false, showInterfaceEdit: false,
      showCreateAmneziaParams: true, showEditAmneziaParams: true,
      interfaceCreate: { settings: { h1: '123' } },
      interfaceEdit: { settings: { h1: '456' } },
    },
    watch: {
      showInterfaceCreate: config.watch.showInterfaceCreate,
      showInterfaceEdit: config.watch.showInterfaceEdit,
    },
  });
  app.showInterfaceCreate = true;
  app.showInterfaceEdit = true;
  await Vue.nextTick();
  assert.equal(app.showCreateAmneziaParams, false);
  assert.equal(app.showEditAmneziaParams, false);
  assert.equal(app.interfaceCreate.settings.h1, '123');
  assert.equal(app.interfaceEdit.settings.h1, '456');
  app.$destroy();
});

test('invalid Amnezia parameters reveal fields before saving either protocol version', async () => {
  for (const protocol of ['amneziawg-2.0', 'amneziawg-3.1']) {
    for (const [model, flag, method] of [
      ['interfaceCreate', 'showCreateAmneziaParams', 'createTunnelInterface'],
      ['interfaceEdit', 'showEditAmneziaParams', 'saveInterfaceEdit'],
    ]) {
      const messages = [];
      const app = {
        [model]: { name: 'test', address: '10.0.0.1/24', protocol, settings: {} },
        [flag]: false, showToast: message => messages.push(message),
        api: new Proxy({}, { get: () => () => assert.fail('invalid parameters must not reach the API') }),
      };
      await interfaceMethods[method].call(app);
      assert.equal(app[flag], true);
      assert.match(messages[0], /H1-H4/);
    }
  }
});

test('inline group creation still selects an existing or newly created group for New client', async () => {
  for (const exists of [true, false]) {
    const group = { id: 'group-1', name: 'friends' };
    let creates = 0;
    const app = {
      inlineGroupInput: 'friends', inlineGroupShowQuick: true, peerCreateGroupId: '',
      clientGroups: exists ? [group] : [], showToast() {},
      api: { async createClientGroup(payload) { assert.equal(payload.name, 'friends'); creates++; return group; } },
      async loadClientGroups() { this.clientGroups = [group]; },
    };
    await aliasMethods.createInlineClientGroup.call(app);
    assert.equal(creates, exists ? 0 : 1);
    assert.equal(app.peerCreateGroupId, group.id);
    assert.equal(app.inlineGroupShowQuick, false);
    assert.equal(app.inlineGroupInput, '');
  }
});

const authSource = await readFile(new URL('www/js/auth.js', import.meta.url), 'utf8');
function loginFixture() {
  const timers = [];
  const sandbox = {
    Promise,
    document: { getElementById: () => null },
    setTimeout: (callback, ms) => { timers.push({ callback, ms }); },
  };
  vm.runInNewContext(authSource.replace('export const authMethods', 'globalThis.authMethods'), sandbox);
  const messages = [];
  let successes = 0;
  const app = {
    ...sandbox.authMethods,
    username: 'owner', password: 'password', totpCode: '123456',
    totpRequired: false, authenticating: false, remember: false,
    showToast: message => messages.push(message),
    async _onLoginSuccess() { successes++; },
  };
  return { app, timers, messages, successes: () => successes };
}
async function flushLoginMicrotasks() {
  for (let i = 0; i < 8; i++) await Promise.resolve();
}

test('password and TOTP login keep feedback visible for one second and reject duplicate submissions', async () => {
  for (const method of ['login', 'loginStep2']) {
    const f = loginFixture();
    let calls = 0;
    f.app.api = {
      async createSession() { calls++; return {}; },
      async verifyTOTP() { calls++; return {}; },
    };
    const pending = f.app[method]();
    await f.app[method]();
    await flushLoginMicrotasks();
    assert.equal(calls, 1);
    assert.equal(f.app.authenticating, true);
    assert.equal(f.successes(), 0);
    assert.equal(f.timers.length, 1);
    assert.equal(f.timers[0].ms, 1000);
    f.timers[0].callback();
    await pending;
    assert.equal(f.app.authenticating, false);
    assert.equal(f.successes(), 1);
  }
});

test('slow login waits for the server without adding another second', async () => {
  const f = loginFixture();
  let resolveRequest;
  f.app.api = { createSession: () => new Promise(resolve => { resolveRequest = resolve; }) };
  const pending = f.app.login();
  await flushLoginMicrotasks();
  f.timers[0].callback();
  await flushLoginMicrotasks();
  assert.equal(f.app.authenticating, true);
  assert.equal(f.successes(), 0);
  resolveRequest({});
  await pending;
  assert.equal(f.successes(), 1);
  assert.equal(f.timers.length, 1);
});

test('login errors release the button after feedback and do not authenticate', async () => {
  for (const method of ['login', 'loginStep2']) {
    const f = loginFixture();
    const fail = () => Promise.reject(new Error('Invalid credentials'));
    f.app.api = { createSession: fail, verifyTOTP: fail };
    const pending = f.app[method]();
    await flushLoginMicrotasks();
    assert.equal(f.messages.length, 0);
    assert.equal(f.app.authenticating, true);
    f.timers[0].callback();
    await pending;
    assert.equal(f.app.authenticating, false);
    assert.equal(f.successes(), 0);
    assert.deepEqual(f.messages, ['Invalid credentials']);
  }
});

test('password login requiring TOTP stays unauthenticated until verification', async () => {
  const f = loginFixture();
  f.app.api = { async createSession() { return { totp_required: true }; } };
  const pending = f.app.login();
  await flushLoginMicrotasks();
  f.timers[0].callback();
  await pending;
  assert.equal(f.app.totpRequired, true);
  assert.equal(f.app.totpCode, '');
  assert.equal(f.app.password, '');
  assert.equal(f.app.authenticating, false);
  assert.equal(f.successes(), 0);
});

const { dashboardMethods } = await import(`data:text/javascript;base64,${Buffer.from(await readFile(new URL('www/js/dashboard.js', import.meta.url), 'utf8')).toString('base64')}`);
test('loading dashboard preserves saved geometry and reset restores nonoverlapping defaults', async () => {
  const saved = [
    { id: 'custom-a', type: 'traffic', x: 1, y: 2, w: 3, h: 4 },
    { id: 'custom-b', type: 'peers', x: 6, y: 2, w: 5, h: 6 },
  ];
  let writes = 0;
  let inits = 0;
  let destroys = 0;
  const app = {
    ...dashboardMethods,
    natRules: [{}], dnatRules: [{}], aliases: [{}], metricsWidgetPeriod: {},
    api: {
      async getDashboardWidgets() { return { widgets: saved }; },
      async putDashboardWidgets(widgets) { writes++; assert.equal(widgets, app.dashWidgets); },
    },
    async $nextTick(callback) { if (callback) callback(); },
    dashInitGrid() { inits++; },
  };
  await app.loadDashboard();
  assert.deepEqual(app.dashWidgets, saved);
  assert.equal(writes, 0, 'opening the dashboard must not rewrite a custom layout');
  assert.equal(inits, 1);
  app.dashGrid = { destroy(removeDOM) { assert.equal(removeDOM, false); destroys++; } };
  app.dashResetLayout();
  assert.equal(destroys, 1);
  assert.equal(inits, 2);
  assert.equal(writes, 1);
  const widgets = app.dashWidgets;
  for (let i = 0; i < widgets.length; i++) {
    for (let j = i + 1; j < widgets.length; j++) {
      const a = widgets[i], b = widgets[j];
      assert.ok(a.x + a.w <= b.x || b.x + b.w <= a.x || a.y + a.h <= b.y || b.y + b.h <= a.y,
        `default widgets ${a.id} and ${b.id} must not overlap`);
    }
  }
});
