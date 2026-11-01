import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { createRequire } from 'node:module';
const Vue = createRequire(import.meta.url)('./www/js/vendor/vue.min.js');
const source = await readFile(new URL('./www/js/awg-generator.js', import.meta.url), 'utf8');
const { generatorMethods, generatorWatchers } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
const apiSource = await readFile(new URL('./www/js/api.js', import.meta.url), 'utf8');
const { API } = await import(`data:text/javascript;base64,${Buffer.from(apiSource).toString('base64')}`);
function setup() {
  let saved;
  const context = {
    ...generatorMethods,
    generateForm: { protocolVersion: '2.0', profile: 'dtls_1_3', saveName: 'sample' },
    showGenerateModal: true,
    loadSettings: async () => {},
    showToast: () => {},
    api: {
      generateTemplate: async options => ({ params: { jc: 0, i1: 'chain' }, protocolVersion: options.protocolVersion, mtu: 1400, resolvedProfile: options.profile, options: { host: 'example.com' }, warnings: [] }),
      createTemplate: async template => { saved = template; },
    },
  };
  return { context, saved: () => saved };
}
test('API preserves version and every explicit false/zero generator option', async () => {
  const options = { protocolVersion: '2.0', junkLevel: 0, useTagR: false, useHeaderProtection: false, mtu: 1400 };
  const api = Object.create(API.prototype);
  api.call = request => request;
  const request = await api.generateTemplate(options);
  assert.deepEqual(request.body, options);
});
test('name changes retain the result and save the server version, MTU and explicit zero', async () => {
  const { context, saved } = setup();
  await context.generateParams();
  context.generateForm.saveName = 'renamed';
  context.invalidateGeneratedProfile();
  assert.ok(context.generatedResult);
  await context.saveGeneratedTemplate();
  assert.deepEqual(saved(), { jc: 0, i1: 'chain', name: 'renamed', isDefault: false, protocolVersion: '2.0', mtu: 1400, host: 'example.com' });
});
test('changing generator inputs invalidates the result and prevents saving or editing it', async () => {
  const { context, saved } = setup();
  await context.generateParams();
  context.generateForm.protocolVersion = '3.1';
  // Even before Vue's asynchronous watcher runs, the save method must refuse.
  await context.saveGeneratedTemplate();
  assert.equal(saved(), undefined);
  context.useGeneratedInForm();
  assert.equal(context.templateForm, undefined);
  context.invalidateGeneratedProfile();
  assert.equal(context.generatedResult, null);
});
test('late results after input changes or closing the dialog are discarded', async () => {
  const { context } = setup();
  let finish;
  context.api.generateTemplate = () => new Promise(resolve => { finish = resolve; });
  const request = context.generateParams();
  context.generateForm.profile = 'stun';
  finish({ params: {} });
  await request;
  assert.equal(context.generatedResult, null);
  assert.equal(context.generatingParams, false);
});

function reactiveGenerator(version) {
  const common = {
    protocolVersion: '2.0', profile: 'random', intensity: 'medium', host: '',
    hostRegion: 'any', browser: 'chrome', useBrowserFp: false,
    clientId: 'amneziavpn', clientRelease: '', mtu: 1500, junkLevel: 5, iterCount: 0,
    mimicAll: false, routerMode: false, useExtremeMax: false,
    useTagC: false, useTagT: true, useTagR: true, useTagRC: true, useTagRD: true,
    useHeaderProtection: false, useContentPadding: false, useRandomTimings: false,
    useRandomTrailers: false, useDisableCookies: false, useNarrowH: false,
    useSameS: false, useDisableH: false,
  };
  const defaults = {
    '2.0': common,
    '3.1': { ...common, protocolVersion: '3.1', mtu: 1280, useHeaderProtection: true,
      useContentPadding: true, useRandomTimings: true, useRandomTrailers: true, useDisableH: true },
  };
  const vm = new Vue({
    data: {
      generatorCatalog: {
        defaults, serverSupportsTagC: true,
        clients: [{ id: 'amneziawg-windows', releases: [{ id: '<2.0.2', limits: {} }],
          limits: { supportsI1I5: true, supportsCpsTagC: false, supportsCpsTagRC: true, supportsCpsTagRD: true } }],
      },
      generateForm: { ...defaults[version], clientId: 'amneziawg-windows', clientRelease: '<2.0.2', saveName: 'Keep this name' },
      generatorUseRecommended: true,
      generatedSignature: '', generatedResult: null, generatedParams: null,
    },
    methods: generatorMethods,
    watch: generatorWatchers,
  });
  return vm;
}

for (const version of ['2.0', '3.1']) {
  for (const [field, value] of Object.entries({ profile: 'wireguard_noise', intensity: 'high',
    mtu: 1400, host: 'example.com', hostRegion: 'ru', browser: 'firefox', useBrowserFp: true,
    junkLevel: 0, iterCount: 4, mimicAll: true, routerMode: true, useExtremeMax: true,
    useTagT: false, useTagR: false, useTagRC: false, useTagRD: false,
    ...(version === '3.1' ? { useHeaderProtection: false, useContentPadding: false,
      useRandomTimings: false, useRandomTrailers: false, useDisableCookies: true,
      useNarrowH: true, useSameS: true, useDisableH: false } : {}),
  })) {
    test(`${version}: changing ${field} clears recommendations; checking restores the full preset`, async () => {
      const vm = reactiveGenerator(version);
      vm.generatedSignature = vm.generatorSignature();
      vm.generatedResult = { params: {} };
      vm.generatedParams = {};
      vm.generateForm[field] = value;
      await Vue.nextTick();
      assert.equal(vm.generatorUseRecommended, false);
      assert.equal(vm.generatedResult, null);
      assert.equal(vm.generatedParams, null);
      vm.generatorUseRecommended = true;
      vm.applyGeneratorRecommendations();
      await Vue.nextTick();
      assert.equal(vm.generatorUseRecommended, true);
      for (const [key, expected] of Object.entries(vm.generatorPreset(version))) {
        assert.equal(vm.generateForm[key], expected, `preset field ${key}`);
      }
      assert.equal(vm.generateForm.clientId, 'amneziawg-windows');
      assert.equal(vm.generateForm.clientRelease, '<2.0.2');
      assert.equal(vm.generateForm.saveName, 'Keep this name');
      assert.equal(vm.generatedResult, null);
      vm.$destroy();
    });
  }
}

test('unchecking keeps manual values; restoring fields by hand does not recheck recommendations', async () => {
  const vm = reactiveGenerator('2.0');
  vm.generatorUseRecommended = false;
  vm.applyGeneratorRecommendations();
  assert.equal(vm.generateForm.intensity, 'medium');
  vm.generateForm.intensity = 'high';
  await Vue.nextTick();
  vm.generateForm.intensity = 'medium';
  await Vue.nextTick();
  assert.equal(vm.generatorUseRecommended, false);
  vm.$destroy();
});

test('changing versions restores all defaults while retaining target client, build and name', async () => {
  const vm = reactiveGenerator('2.0');
  vm.generateForm.profile = 'dns_query';
  vm.generateForm.intensity = 'high';
  await Vue.nextTick();
  for (const version of ['3.1', '2.0']) {
    vm.generateForm.protocolVersion = version;
    await Vue.nextTick();
    assert.equal(vm.generatorUseRecommended, true);
    assert.equal(vm.generateForm.profile, 'random');
    assert.equal(vm.generateForm.intensity, 'medium');
    assert.equal(vm.generateForm.mtu, version === '3.1' ? 1280 : 1500);
    assert.equal(vm.generateForm.clientId, 'amneziawg-windows');
    assert.equal(vm.generateForm.clientRelease, '<2.0.2');
    assert.equal(vm.generateForm.saveName, 'Keep this name');
  }
  vm.$destroy();
});

test('recommendations obey both client and server tag support', async () => {
  const vm = reactiveGenerator('3.1');
  vm.generatorCatalog.defaults['3.1'].useTagC = true;
  vm.generatorUseRecommended = true;
  vm.applyGeneratorRecommendations();
  await Vue.nextTick();
  assert.equal(vm.generateForm.useTagC, false);
  vm.generatorCatalog.clients[0].limits.supportsCpsTagC = true;
  vm.generatorCatalog.serverSupportsTagC = false;
  vm.applyGeneratorRecommendations();
  await Vue.nextTick();
  assert.equal(vm.generateForm.useTagC, false);
  vm.generatorCatalog.clients[0].limits.supportsI1I5 = false;
  vm.applyGeneratorRecommendations();
  await Vue.nextTick();
  for (const key of ['useTagC', 'useTagT', 'useTagR', 'useTagRC', 'useTagRD']) assert.equal(vm.generateForm[key], false);
  assert.equal(vm.generatorUseRecommended, true);
  vm.$destroy();
});
