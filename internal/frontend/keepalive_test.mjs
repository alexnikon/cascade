import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const source = await readFile(new URL('./www/js/peers.js', import.meta.url), 'utf8');
const { peerMethods: methods } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

const cases = [
  ['amneziawg-3.1', '', '5-25', { persistentKeepaliveV3: '5-25' }],
  ['amneziawg-3.1', '5-25', '011-017', { persistentKeepaliveV3: '11-17' }],
  ['amneziawg-3.1', '5-25', '5-25', {}],
  [null, '5-25', '5-25', {}],
  ['amneziawg-3.1', '5-25', '30', { persistentKeepalive: 30, persistentKeepaliveV3: '' }],
  ['amneziawg-3.1', '5-25', '25', { persistentKeepalive: 25, persistentKeepaliveV3: '' }],
  ['amneziawg-3.1', '5-25', '0', { persistentKeepalive: 0, persistentKeepaliveV3: '' }],
  ['amneziawg-3.1', '', '', null],
  ['amneziawg-3.1', '', '9-2', null],
  ['amneziawg-3.1', '', '0-2', null],
  ['amneziawg-3.1', '', '65536', null],
  ['amneziawg-2.0', '', '0', { persistentKeepalive: 0 }],
  ['amneziawg-2.0', '', '5-25', null],
  ['wireguard-1.0', '', '30', { persistentKeepalive: 30 }],
  ['wireguard-1.0', '', '2.5', null],
];

for (const [protocol, oldRange, input, expected] of cases) {
  test(`persistent keepalive on ${protocol || 'a peer awaiting interface metadata'}: ${oldRange || '25'} to ${input || '(blank)'}`, async () => {
    let payload;
    let toast;
    const context = {
      ...methods,
      activeInterfaceId: 'wg10',
      tunnelInterfaces: protocol ? [{ id: 'wg10', protocol }] : [],
      expiryDateTimeForInput: () => '',
      _refreshPeersOrAll: async () => {},
      showToast: (_, type) => { toast = type; },
      api: { updateTunnelInterfacePeer: async body => { payload = body; } },
    };
    const peer = { id: 'peer', interfaceId: 'wg10', peerType: 'interconnect', persistentKeepalive: 25, persistentKeepaliveV3: oldRange };
    context.openPeerEdit(peer);
    assert.equal(context.peerEditForm.persistentKeepalive, oldRange || '25');
    context.peerEditForm.persistentKeepalive = input;
    await context.savePeerEdit();
    if (expected === null) {
      assert.equal(payload, undefined);
      assert.equal(toast, 'error');
      return;
    }
    assert.equal(toast, 'success');
    const keepaliveFields = Object.fromEntries(Object.entries(payload).filter(([key]) => key.startsWith('persistentKeepalive')));
    assert.deepEqual(keepaliveFields, expected);
  });
}
