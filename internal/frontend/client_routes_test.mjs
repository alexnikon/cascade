import { readFile } from 'node:fs/promises';
import assert from 'node:assert/strict';
import test from 'node:test';

const source = await readFile(new URL('www/js/peers.js', import.meta.url), 'utf8');
const { peerMethods } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

for (const mode of ['manual', 'exclude-ipset']) {
  test(`client editor sends ${mode} source and preserves the manual list`, async () => {
    let payload;
    const context = {
      peerEditForm: { _peer: { id: 'client-1', interfaceId: 'wg10', peerType: 'client' }, name: 'Client', clientAllowedIPs: '192.0.2.0/24', clientAllowedIPsMode: mode, clientAllowedIPsAliasId: 'ipset-1', persistentKeepalive: 25 },
      expiryDateTimeToUTC: () => '',
      _peerIfaceId: peer => peer.interfaceId,
      _refreshPeersOrAll: async () => {}, loadClientGroups: () => {}, loadAliases: () => {}, showToast: () => {},
      api: { updateTunnelInterfacePeer: async body => { payload = body; } },
    };
    await peerMethods.savePeerEdit.call(context);
    assert.equal(payload.clientAllowedIPs, '192.0.2.0/24');
    assert.equal(payload.clientAllowedIPsMode, mode);
    assert.equal(payload.clientAllowedIPsAliasId, mode === 'manual' ? '' : 'ipset-1');
    assert.equal(context.peerMutationInFlight, false);
  });
}
