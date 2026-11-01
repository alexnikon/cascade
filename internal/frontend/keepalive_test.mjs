import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const source = await readFile(new URL('./www/js/peers.js', import.meta.url), 'utf8');
const { peerMethods: methods } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

for (const protocol of ['amneziawg-3.1', 'amneziawg-2.0', 'wireguard-1.0']) {
  test(`keepalive range editing on ${protocol}`, async () => {
    let payload;
    const context = {
      ...methods,
      activeInterfaceId: 'wg10',
      tunnelInterfaces: [{ id: 'wg10', protocol }],
      expiryDateTimeForInput: () => '',
      _refreshPeersOrAll: async () => {},
      showToast: () => {},
      api: { updateTunnelInterfacePeer: async body => { payload = body; } },
    };
    const peer = { id: 'peer', interfaceId: 'wg10', peerType: 'interconnect', persistentKeepalive: 25, persistentKeepaliveV3: '5-25' };
    context.openPeerEdit(peer);
    assert.equal(context.peerEditForm.persistentKeepaliveV3, '5-25');
    assert.equal(context.peerSupportsKeepaliveRange(peer), protocol === 'amneziawg-3.1');
    context.peerEditForm.persistentKeepaliveV3 = '';
    await context.savePeerEdit();
    assert.equal(payload.persistentKeepalive, 25);
    if (protocol === 'amneziawg-3.1') assert.equal(payload.persistentKeepaliveV3, '');
    else assert.equal(Object.hasOwn(payload, 'persistentKeepaliveV3'), false);
  });
}
