import { readFile } from 'node:fs/promises';
import assert from 'node:assert/strict';
import test from 'node:test';

const source = await readFile(new URL('www/js/select-width.js', import.meta.url), 'utf8');
const { contentSelectDirective: directive } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

function fixture() {
  const changes = new Map(), resizes = new Map(), classes = new Set();
  let measurements = 0;
  const computed = { fontSize: '16px', fontFamily: 'system-ui', fontWeight: '400',
    paddingLeft: '12px', paddingRight: '12px', borderLeftWidth: '1px', borderRightWidth: '1px' };
  const document = {
    defaultView: {
      getComputedStyle: () => computed,
      addEventListener: (name, fn) => resizes.set(name, fn),
      removeEventListener: (name, fn) => { assert.equal(resizes.get(name), fn); resizes.delete(name); },
    },
    createElement: () => ({
      style: {}, textContent: '', remove() {},
      getBoundingClientRect() { measurements++; return { width: this.textContent.length * 8 }; },
    }),
    body: { appendChild() {} },
  };
  const el = {
    ownerDocument: document, options: [{ text: 'Free' }, { text: 'Administrators and support' }], selectedIndex: 0,
    multiple: false, size: 0, style: {}, classList: { add: name => classes.add(name) },
    addEventListener: (name, fn) => changes.set(name, fn),
    removeEventListener: (name, fn) => { assert.equal(changes.get(name), fn); changes.delete(name); },
  };
  return { el, document, computed, changes, resizes, classes, measurements: () => measurements };
}
async function flush() { await Promise.resolve(); await Promise.resolve(); }

test('select follows the selected label through Vue updates and native changes', async () => {
  const f = fixture();
  f.el.options.push({ text: 'A hidden option that must not determine the width', hidden: true });
  directive.inserted(f.el);
  await flush();
  const width = f.el.style.width;
  assert.equal(width, '98px', 'selected label includes padding, border, arrow and breathing room');
  assert.ok(f.classes.has('content-select'));
  f.el.selectedIndex = 1;
  directive.componentUpdated(f.el);
  await flush();
  assert.equal(f.el.style.width, '274px');
  assert.equal(f.measurements(), 2);
  f.el.options[1].text += ' with a longer group name';
  directive.componentUpdated(f.el);
  await flush();
  assert.ok(Number.parseInt(f.el.style.width) > Number.parseInt(width));
  f.el.options.splice(1, 1);
  f.el.selectedIndex = 0;
  f.changes.get('change')();
  await flush();
  assert.equal(f.el.style.width, '98px');
  directive.unbind(f.el);
});

test('repeated Vue updates reuse handlers and avoid unnecessary measurements', async () => {
  const f = fixture();
  directive.inserted(f.el);
  directive.componentUpdated(f.el);
  directive.componentUpdated(f.el);
  await flush();
  assert.equal(f.measurements(), 1);
  directive.componentUpdated(f.el);
  await flush();
  assert.equal(f.measurements(), 1);
  assert.equal(f.changes.size, 1);
  assert.equal(f.resizes.size, 1);
  f.computed.fontSize = '20px';
  f.resizes.get('resize')();
  await flush();
  assert.equal(f.el.style.width, '106px');
  assert.equal(f.measurements(), 2);
  directive.unbind(f.el);
  assert.equal(f.changes.size, 0);
  assert.equal(f.resizes.size, 0);
});

test('removing a select cancels queued measurements and late font callbacks', async () => {
  const f = fixture();
  let loaded;
  f.document.fonts = { ready: new Promise(resolve => { loaded = resolve; }) };
  directive.inserted(f.el);
  directive.unbind(f.el);
  loaded();
  await flush();
  assert.equal(f.measurements(), 0);
  assert.equal(f.changes.size, 0);
  assert.equal(f.resizes.size, 0);
  directive.componentUpdated(f.el);
  await flush();
  assert.equal(f.measurements(), 0);
});

test('font loading remeasures even when the CSS font family is unchanged', async () => {
  const f = fixture();
  let loaded;
  f.document.fonts = { ready: new Promise(resolve => { loaded = resolve; }) };
  directive.inserted(f.el);
  await flush();
  assert.equal(f.measurements(), 1);
  loaded();
  await flush();
  assert.equal(f.measurements(), 2);
  directive.unbind(f.el);
});

test('multiple selects and visible listboxes retain their own sizing', async () => {
  for (const property of ['multiple', 'size']) {
    const f = fixture();
    f.el[property] = property === 'multiple' ? true : 5;
    directive.inserted(f.el);
    await flush();
    assert.equal(f.measurements(), 0);
    assert.equal(f.classes.size, 0);
    directive.unbind(f.el);
  }
});

test('unselected label changes do not resize the control and empty selection is supported', async () => {
  const f = fixture();
  directive.inserted(f.el);
  await flush();
  f.el.options[1].text += ' longer';
  directive.componentUpdated(f.el);
  await flush();
  assert.equal(f.measurements(), 1);
  assert.equal(f.el.style.width, '98px');
  f.el.selectedIndex = -1;
  directive.componentUpdated(f.el);
  await flush();
  assert.equal(f.el.style.width, '66px');
  directive.unbind(f.el);
});
