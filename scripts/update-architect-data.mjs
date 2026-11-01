// Export the pinned Architect catalogues without adding a runtime JS dependency.
// Requires Node 24+ and a checkout of Any-Tech-ARCHITECT v4.5.1.
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { execFileSync } from 'node:child_process';
const root = resolve(process.argv[2] || '../Any-Tech-ARCHITECT');
const revision = '0b9d3b7aa6c1661daa29b92010f841a3c04ca9fb';
if (execFileSync('git', ['-C', root, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim() !== revision) throw Error('Architect checkout must match the pinned revision');
registerHooks({
  resolve(specifier, context, next) {
    if (specifier.startsWith('@/')) specifier = pathToFileURL(resolve(root, 'src', specifier.slice(2))).href;
    if (specifier.startsWith('file:') || specifier.startsWith('.')) {
      const url = new URL(specifier, context.parentURL);
      if (!/\.[a-z]+$/i.test(url.pathname)) url.pathname += existsSync(fileURLToPath(url) + '.ts') ? '.ts' : '/index.ts';
      return next(url.href, context);
    }
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url.endsWith('.ts')) return { format: 'module', source: stripTypeScriptTypes(readFileSync(new URL(url), 'utf8'), { mode: 'strip' }), shortCircuit: true };
    return next(url, context);
  },
});
if (process.argv.includes('--fixtures')) {
  Object.defineProperty(globalThis, 'crypto', { value: { getRandomValues(array) { array.fill(0); return array; } }, configurable: true });
  Date.now = () => 1700000000000;
}
const load = (path) => import(pathToFileURL(resolve(root, 'src', path)).href);
const { AWG_CLIENT_PROFILES } = await load('engines/awg/generator/clients.ts');
const { BROWSER_FINGERPRINTS } = await load('shared/fingerprints.ts');
const { hostsFor } = await load('shared/domains.ts');
const { en } = await load('i18n/locales/en.ts');
const clients = AWG_CLIENT_PROFILES.map(c => ({ ...c, notes: c.notes.map(n => en[n]), releases: (c.releases || []).map(r => ({ ...r, label: en[r.label].replace('{version}', r.labelParams?.version || ''), notes: (r.notes || []).map(n => en[n]) })) }));
const pools = {};
for (const region of ['any', 'ru', 'global', 'eu', 'cn', 'by', 'uk']) {
  for (const role of ['quic', 'tls', 'dtls', 'sip', 'stun', 'dns']) {
    for (const dnsType of role === 'dns' ? ['A', 'AAAA'] : ['']) {
      const query = { regions: region === 'any' ? undefined : [region], role, dnsType: dnsType || undefined };
      let names = hostsFor(query);
      let relaxed = false;
      if (!names.length) { names = hostsFor({ ...query, allowUnknown: true }); relaxed = true; }
      if (!names.length) { names = hostsFor({ role, allowUnknown: true }); relaxed = true; }
      pools[`${region}|${role}|${dnsType}`] = { hosts: names, relaxed };
    }
  }
}
const output = resolve(dirname(fileURLToPath(import.meta.url)), '../internal/awgparams/architect-data.json');
writeFileSync(output, JSON.stringify({ version: '4.5.1', revision, clients, browsers: BROWSER_FINGERPRINTS.filter(b => b.sizes), pools }, null, 2) + '\n');
console.log(`Updated ${output}`);

// A zero-filled entropy source makes minimum-bound reference cases reproducible
// across the TypeScript and Go implementations without sharing RNG machinery.
if (process.argv.includes('--fixtures')) {
  const { genCfg } = await load('engines/awg/generator/index.ts');
  const { versionPreset } = await load('engines/awg/generator/presets.ts');
  const cases = [];
  for (const version of ['2.0', '3.1']) {
    for (const profile of ['quic_initial', 'quic_0rtt', 'http3', 'tls_client_hello', 'dtls_1_2', 'dtls_1_3', 'wireguard_noise', 'sip', 'dns_query', 'stun', 'tls_to_quic', 'quic_burst']) {
      for (const mode of ['default', 'mimic', 'static', 'router', 'extreme', 'equal', 'narrow']) {
        const input = { version, intensity: 'medium', profile, customHost: 'example.com', mimicAll: mode === 'mimic', useTagC: mode !== 'static', useTagT: mode !== 'static', useTagR: mode !== 'static', useTagRC: mode !== 'static', useTagRD: mode !== 'static', useBrowserFp: false, browserProfile: 'chrome', junkLevel: 5, iterCount: 0, routerMode: mode === 'router', useExtremeMax: mode === 'extreme', hostRegion: 'any', clientId: 'awg-kmod', clientRelease: null, ...versionPreset(version) };
        if (mode === 'equal') input.useSameS = true;
        if (mode === 'narrow') { input.useDisableH = false; input.useNarrowH = true; }
        const config = genCfg(input);
        const { version: unusedVersion, h1s, h2s, h3s, h4s, awg3, ...params } = config;
        if (awg3) for (const [key, value] of Object.entries(awg3)) if (value !== '') params[key] = value;
        const { version: v, customHost, browserProfile, clientRelease, ...options } = input;
        cases.push({ name: `${version}/${profile}/${mode}`, options: { ...options, protocolVersion: v, host: customHost, browser: browserProfile, clientRelease: clientRelease || '' }, params });
      }
    }
  }
  writeFileSync(resolve(dirname(output), 'architect-fixtures.json'), JSON.stringify(cases, null, 2) + '\n');
  console.log(`Exported ${cases.length} upstream reference cases`);
}
