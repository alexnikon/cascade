import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import test from 'node:test';

const source = await readFile(new URL('www/js/diagnostics.js', import.meta.url), 'utf8');
function fixture() {
  const streams = [], messages = [];
  class EventSource {
    constructor(url) { this.url = url; streams.push(this); }
    close() { this.closed = true; }
    line(data) { this.onmessage({ data }); }
  }
  const context = { EventSource, URLSearchParams, URL,
    window: { location: { origin: 'http://cascade.test', pathname: '/hidden/' } },
    setTimeout: fn => { fn(); },
  };
  vm.createContext(context);
  vm.runInContext(source.replace('export const diagnosticsMethods', 'globalThis.methods'), context);
  const state = { ...context.methods, api: {},
    pingHost: '127.0.0.1', pingSource: 'lo', pingCount: 3, pingSize: 64, pingTos: 16, pingDf: true,
    traceHost: '127.0.0.1', traceType: 'tcp', traceSource: 'lo',
    terminalLines: [], $nextTick(fn) { fn(); }, showToast: message => messages.push(message),
  };
  return { state, streams, messages };
}

test('Diagnostics loads utilities directly and Dashboard keeps its graph configuration', async () => {
  const view = await readFile(new URL('templates/views/diagnostics.html', import.meta.url), 'utf8');
  const header = await readFile(new URL('templates/components/header.html', import.meta.url), 'utf8');
  assert.doesNotMatch(view, /diagActiveTab|diagWidgets|apexchart|Add Graph|[Tt]cpdump/);
  assert.doesNotMatch(header, /diagActiveTab|diagAddWidget|>\s*(Graphs|Utilities)\s*</);
  const dashboard = await readFile(new URL('templates/views/dashboard.html', import.meta.url), 'utf8');
  assert.match(dashboard, /apexchart/);
  assert.match(dashboard, /metricsOpenConfig/);
});

test('Ping and Traceroute pass the selected interface and retain the proxy prefix', () => {
  const f = fixture();
  f.state.pingStart();
  const ping = new URL(f.streams[0].url);
  assert.equal(ping.pathname, '/hidden/api/diagnostics/ping/stream');
  for (const [key, value] of Object.entries({ host: '127.0.0.1', source: 'lo', count: '3', size: '64', tos: '16', df: 'true' })) assert.equal(ping.searchParams.get(key), value);
  f.state.traceStart();
  const trace = new URL(f.streams[1].url);
  assert.equal(trace.searchParams.get('source'), 'lo');
  assert.equal(trace.searchParams.get('type'), 'tcp');
  f.state.pingStop(); f.state.traceStop();
  assert.equal(f.streams[0].closed, true);
  assert.equal(f.streams[1].closed, true);
});

test('leaving Diagnostics closes every stream and ignores stale callbacks', () => {
  const f = fixture(); f.state.pingStart(); f.state.traceStart();
  f.state.stopDiagnostics();
  for (const stream of f.streams) { assert.equal(stream.closed, true); stream.line('stale output'); }
  assert.equal(f.state.terminalLines.length, 0);
  assert.equal(f.state.pingRunning, false);
  assert.equal(f.state.traceRunning, false);
});
