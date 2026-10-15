"""Minimal TLS-verified HTTP/3 probe for the isolated network test."""
import asyncio
import sys

from aioquic.asyncio import QuicConnectionProtocol, connect
from aioquic.h3.connection import H3_ALPN, H3Connection
from aioquic.h3.events import HeadersReceived, DataReceived
from aioquic.quic.configuration import QuicConfiguration


class Probe(QuicConnectionProtocol):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.http = H3Connection(self._quic)
        self.finished = asyncio.get_running_loop().create_future()
        self.status = None

    def quic_event_received(self, event):
        for message in self.http.handle_event(event):
            if isinstance(message, HeadersReceived):
                for name, value in message.headers:
                    if name == b':status':
                        self.status = int(value)
            if isinstance(message, (HeadersReceived, DataReceived)) and message.stream_ended:
                if not self.finished.done():
                    self.finished.set_result(self.status)

    async def get(self, host, path, token):
        self.finished = asyncio.get_running_loop().create_future()
        self.status = None
        stream = self._quic.get_next_available_stream_id()
        headers = [(b':method', b'GET'), (b':scheme', b'https'),
                   (b':authority', host.encode()), (b':path', path.encode())]
        if token:
            headers.append((b'authorization', ('Bearer ' + token).encode()))
        self.http.send_headers(stream, headers, end_stream=True)
        self.transmit()
        return await self.finished


async def main():
    host, path = sys.argv[1:3]
    token = sys.argv[3] if len(sys.argv) > 3 else ''
    config = QuicConfiguration(is_client=True, alpn_protocols=H3_ALPN)
    config.load_verify_locations('/certs/server.crt')
    async with connect(host, 443, configuration=config, create_protocol=Probe) as client:
        print(await client.get(host, path, token))


if __name__ == '__main__':
    asyncio.run(asyncio.wait_for(main(), timeout=5))
