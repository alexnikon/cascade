import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import vm from 'node:vm';

const source = await readFile(new URL('www/js/notifications.js', import.meta.url), 'utf8');

test('newest toast appears first and each toast keeps its own dismissal timing', () => {
  const timers = [];
  const context = vm.createContext({
    setTimeout(callback, duration) { timers.push({ callback, duration }); },
  });
  vm.runInContext(source.replace('export const notificationMethods', 'globalThis.methods'), context);
  const app = { ...context.methods, toasts: [] };

  app.showToast('Saved');
  app.showToast('Failed', 'error');
  app.showToast('Update available', 'info', 1200);

  assert.deepEqual(app.toasts.map(toast => toast.duration), [1200, 0, 5000]);
  assert.deepEqual(timers.map(timer => timer.duration), [5000, 1200]);
  app.dismissToast(app.toasts[0].id);
  assert.deepEqual(app.toasts.map(toast => toast.message), ['Failed', 'Saved']);
  timers[1].callback();
  timers[0].callback();
  assert.deepEqual(app.toasts.map(toast => toast.message), ['Failed']);
  app.dismissToast(app.toasts[0].id);
  assert.equal(app.toasts.length, 0);
});
