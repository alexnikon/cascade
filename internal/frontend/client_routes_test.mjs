import { readFile } from 'node:fs/promises';
import assert from 'node:assert/strict';
import test from 'node:test';

const source = await readFile(new URL('www/js/peers.js', import.meta.url), 'utf8');
const { peerMethods } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

test('client editor saves a manual list without routing mode or alias fields', async () => {
  let payload;
  const context = {
    ...peerMethods,
    peerEditForm: { _peer: { id: 'client-1', interfaceId: 'wg10', peerType: 'client' }, name: 'Client', clientAllowedIPs: '192.0.2.0/24', persistentKeepalive: 25 },
    expiryDateTimeToUTC: () => '',
    _peerIfaceId: peer => peer.interfaceId,
    _refreshPeersOrAll: async () => {}, loadClientGroups: () => {}, loadAliases: () => {}, showToast: () => {},
    api: { updateTunnelInterfacePeer: async body => { payload = body; } },
  };
  await peerMethods.savePeerEdit.call(context);
  assert.equal(payload.clientAllowedIPs, '192.0.2.0/24');
  assert.equal(Object.hasOwn(payload, 'clientAllowedIPsMode'), false);
  assert.equal(Object.hasOwn(payload, 'clientAllowedIPsAliasId'), false);
  assert.equal(context.peerMutationInFlight, false);
});
