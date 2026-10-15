"""Verify request authorization after a QUIC connection changes VPN ingress."""
import asyncio
import os
import socket

from aioquic.asyncio import connect
from aioquic.h3.connection import H3_ALPN
from aioquic.quic.configuration import QuicConfiguration
from web_access_http3 import Probe


async def main():
    with open('/var/run/netns/admin') as administrative, open('/var/run/netns/client') as other:
        os.setns(administrative.fileno(), os.CLONE_NEWNET)
        config = QuicConfiguration(is_client=True, alpn_protocols=H3_ALPN)
        config.load_verify_locations('/certs/server.crt')
        async with connect('198.51.100.10', 443, configuration=config, create_protocol=Probe) as client:
            assert await client.get('198.51.100.10', '/test-admin/', '') == 200
            os.setns(other.fileno(), os.CLONE_NEWNET)
            transport, _ = await asyncio.get_running_loop().create_datagram_endpoint(
                lambda: client, local_addr=('::ffff:10.10.1.2', 0), family=socket.AF_INET6)
            try:
                client._quic.change_connection_id()
                client.transmit()
                await client.ping()
                await asyncio.sleep(0.2)
                status = await client.get('198.51.100.10', '/test-admin/', '')
                assert status == 404, f'Migrated connection retained administrative access: {status}'
                print('PASS: QUIC migration cannot retain administrative VPN access')
            finally:
                client.close()
                await client.wait_closed()
                transport.close()


if __name__ == '__main__':
    asyncio.run(asyncio.wait_for(main(), timeout=8))
