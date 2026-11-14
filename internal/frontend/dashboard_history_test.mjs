import assert from 'node:assert/strict';
import test from 'node:test';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
const source = await readFile(new URL('www/js/dashboard.js', import.meta.url), 'utf8');
function fixture() {
  const document = { hidden: false, addEventListener() {} };
  const context = vm.createContext({ document, console, setInterval, clearInterval, setTimeout, clearTimeout });
  vm.runInContext(source.replace('export const dashboardMethods', 'globalThis.methods'), context);
  const state = { ...context.methods, activePage: 'dashboard', dashWidgets: [{ id:'w',type:'monitoring',graphs:['cpu'] }], diagWidgets: [],
    metricsWidgetPeriod: {}, metricsHistory: {}, metricsAreaSeriesCache: {}, metricsChartHeights: {},
    $set(o,k,v) { o[k]=v; }, api: {}, metricsKeyLabel: k => k };
  return { state, document };
}
test('five minute history merges server samples and concurrent live samples without duplicates', async () => {
  const {state:s}=fixture(); const now=Math.floor(Date.now()/5000)*5000;
  s.metricsHistory['w:cpu']=[{x:now,y:7}];
  s.api.getMetricsHistory=async()=>({points:[[now-10000,2],[now,8],[now-400000,1]]});
  await s.metricsLoadHistory('w','cpu','5m');
  assert.equal(s.metricsHistory['w:cpu'].length,2);
  assert.equal(s.metricsHistory['w:cpu'][1].y,8);
});
test('stale responses cannot populate removed widgets or different periods/pages', async () => {
  for (const mutate of [s=>s.activePage='interfaces', s=>s.dashWidgets=[], s=>s.metricsWidgetPeriod.w='1h', s=>s._metricsEpoch=1]) {
    const {state:s}=fixture(); let resolve;
    s.api.getMetricsHistory=()=>new Promise(r=>resolve=r);
    const p=s.metricsLoadHistory('w','cpu','5m'); mutate(s); resolve({points:[[Date.now(),4]]}); await p;
    assert.equal(s.metricsHistory['w:cpu'],undefined);
  }
});
test('disabled history and API failure preserve live points', async () => {
  const {state:s}=fixture(); const now=Date.now(); s.metricsHistory['w:cpu']=[{x:now,y:4}];
  s.api.getMetricsHistory=async()=>({points:[]}); await s.metricsLoadHistory('w','cpu','5m');
  assert.equal(s.metricsHistory['w:cpu'][0].y,4);
  s.api.getMetricsHistory=async()=>{throw Error('offline');}; await s.metricsLoadHistory('w','cpu','5m');
  assert.equal(s.metricsHistory['w:cpu'][0].y,4);
});
test('missing live samples produce a visible gap', () => {
  const {state:s}=fixture(); s.metricsHistory['w:cpu']=[{x:10000,y:1},{x:40000,y:2}];
  s._updateAreaSeriesCache('w','cpu'); assert.equal(s.metricsAreaSeriesCache['w:cpu'][0].data[1].y,null);
});
test('identical pending requests are shared', async () => {
  const {state:s}=fixture(); let resolve, calls=0;
  s.api.getMetricsHistory=()=>{calls++; return new Promise(r=>resolve=r);};
  const a=s.metricsHistoryRequest('getMetricsHistory','cpu','5m'), b=s.metricsHistoryRequest('getMetricsHistory','cpu','5m');
  resolve({points:[]}); await Promise.all([a,b]); assert.equal(calls,1);
});
test('hidden tabs and unrelated pages do not start polling', () => {
  const {state:s,document:d}=fixture(); d.hidden=true; s.metricsStartPoller(); assert.equal(s.metricsPoller,undefined);
  d.hidden=false; s.activePage='interfaces'; s._metricsVisibilityHandler(); assert.equal(s.metricsPoller,undefined);
});
test('graphs share available height with a readable minimum', () => {
  const {state:s}=fixture(); s.metricsChartHeights.w=360; assert.equal(s.metricsChartHeight('w'),360);
  s.dashWidgets[0].graphs.push('mem'); assert.equal(s.metricsChartHeight('w'),180);
  s.metricsChartHeights.w=100; assert.equal(s.metricsChartHeight('w'),90);
});
