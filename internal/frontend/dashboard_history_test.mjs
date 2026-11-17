import assert from 'node:assert/strict';
import test from 'node:test';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
const source = await readFile(new URL('www/js/dashboard.js', import.meta.url), 'utf8');
function fixture() {
  const document = { hidden: false, addEventListener() {} };
  const context = vm.createContext({ document, console, setInterval, clearInterval, setTimeout, clearTimeout });
  vm.runInContext(source.replace('export const dashboardMethods', 'globalThis.methods'), context);
  const state = { ...context.methods, activePage: 'dashboard', dashWidgets: [{ id:'w',type:'monitoring',graphs:['cpu'] }],
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
  d.hidden=false; for (const page of ['interfaces','diagnostics']) { s.activePage=page; s._metricsVisibilityHandler(); assert.equal(s.metricsPoller,undefined); }
});
test('one or two graphs fit the body while larger stacks retain a readable minimum', () => {
  const {state:s}=fixture();
  s.metricsChartHeights.w=360;
  assert.equal(s.metricsChartHeight('w'),360);
  s.dashWidgets[0].graphs.push('mem');
  assert.equal(s.metricsChartHeight('w'),176);
  s.metricsChartHeights.w=170;
  assert.equal(s.metricsChartHeight('w'),81);
  s.metricsChartHeights.w=171;
  assert.equal(s.metricsChartHeight('w')*2+8 <= 171,true);
  s.dashWidgets[0].graphs.push('eth0:rx');
  assert.equal(s.metricsChartHeight('w'),90);
  s.metricsChartHeights.w=400;
  assert.equal(s.metricsChartHeight('w'),128);
  s.dashWidgets[0].graphs=['cpu'];
  assert.equal(s.metricsChartHeight('w'),400);
});

test('peers filter and sort save, reload and reset preserve the widget layout', async () => {
  const { state: s } = fixture();
  s.dashWidgets = [{ id: 'peers', type: 'peers', x: 2, fontScale: 1.2 }];
  s.dashPeersState = {};
  s.showToast = () => {};
  let persisted;
  s.api.putDashboardWidgets = async widgets => { persisted = JSON.parse(JSON.stringify(widgets)); };
  const view = s.dashPeersGetState('peers');
  view.iface = 'long-interface-name';
  view.sort = 'seen';
  assert.equal(s.dashPeersViewDirty('peers'), true);
  await s.dashSavePeersView('peers');
  assert.equal(s.dashPeersViewDirty('peers'), false);
  s.dashWidgets = persisted;
  s.dashPeersState = {};
  assert.equal(s.dashPeersGetState('peers').iface, 'long-interface-name');
  assert.equal(s.dashPeersGetState('peers').sort, 'seen');
  await s.dashResetPeersView('peers');
  assert.equal(s.dashPeersGetState('peers').iface, '');
  assert.equal(s.dashPeersGetState('peers').sort, 'name');
  assert.equal(persisted[0].peerFilter, undefined);
  assert.equal(persisted[0].peerSort, undefined);
  assert.equal(persisted[0].x, 2);
  assert.equal(persisted[0].fontScale, 1.2);
});

test('peers header owns the borderless view action and editors are absent', async () => {
  const dashboard = await readFile(new URL('templates/views/dashboard.html', import.meta.url), 'utf8');
  const header = dashboard.slice(dashboard.indexOf('class="dash-card-header"'), dashboard.indexOf('class="dash-card-body"'));
  assert.ok(header.indexOf('dash-peers-view-action') < header.indexOf('dashAdjustFontScale'));
  assert.match(header, /@pointerdown\.stop @mousedown\.stop @touchstart\.stop/);
  assert.match(header, /@click\.stop="dashPeersViewDirty/);
  const toolbar = dashboard.slice(dashboard.indexOf('class="dash-peers-toolbar"'), dashboard.indexOf('<!-- scrollable peer list -->'));
  assert.equal((toolbar.match(/<select /g) || []).length, 2);
  assert.doesNotMatch(toolbar, /<button/);
  const interfaces = await readFile(new URL('templates/views/interfaces.html', import.meta.url), 'utf8');
  assert.doesNotMatch(interfaces, /peerEditName|peerEditExpireDate|updatePeerName|updatePeerExpireDate/);
  assert.match(interfaces, /updatePeerAddress/);
  assert.match(interfaces, /peer\.expiredAt \| expiredDateFormat/);
  const css = await readFile(new URL('www/css/app.css', import.meta.url), 'utf8');
  assert.match(css, /\.dash-peers-toolbar \{[^}]*display: flex;[^}]*flex-wrap: wrap;[^}]*justify-content: flex-start;/);
  assert.match(css, /\.dash-peers-view-action \{[^}]*border: none;/);
  assert.match(css, /\.dash-peers-view-action:focus-visible/);
});
