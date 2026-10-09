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
