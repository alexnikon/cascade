import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

async function methods(file, name) {
  const source = (await readFile(new URL(`./www/js/${file}`, import.meta.url), 'utf8'))
    .replace("import { i18n } from './i18n.js';", 'const i18n = {};');
  return (await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`))[name];
}
const interfaces = await methods('interfaces.js', 'interfaceMethods');
const settings = await methods('settings.js', 'settingsMethods');

for (const version of ['2.0', '3.1']) {
  for (const mtu of [0, 1380]) {
    test(`AWG ${version}: generation and template selection preserve interface MTU ${mtu}`, async () => {
      const calls = [];
      const context = {
        ...interfaces, ...settings,
        interfaceCreate: { protocol: `amneziawg-${version}`, mtu, settings: {} },
        templates: [{ id: 'profile', protocolVersion: version, mtu: 1500, jc: 7, h1: '1-2' }],
        api: { generateTemplate: async options => {
          calls.push(options);
          return { params: { jc: 5, h1: '3-4' }, mtu: version === '3.1' ? 1280 : 1500 };
        } },
        showToast: () => {},
      };
      await context.generateAndFillInterfaceParams();
      assert.equal(calls[0].protocolVersion, version);
      assert.equal(context.interfaceCreate.settings.jc, 5);
      assert.equal(context.interfaceCreate.mtu, mtu);
      context.onInterfaceTemplateSelect('profile');
      assert.equal(context.interfaceCreate.settings.jc, 7);
      assert.equal(context.interfaceCreate.mtu, mtu);
    });
  }
}

test('opening a new form resets a prior explicit MTU to auto', () => {
  const context = {
    ...interfaces,
    interfaceCreate: { mtu: 1500 },
    generateHeaderProtectionKey: () => 'test-key',
  };
  context._resetInterfaceCreate();
  assert.equal(context.interfaceCreate.mtu, 0);
});
