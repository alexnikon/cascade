"""Test-only HTTP loopback entry; upstream Caddy TLS is still verified."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ssl
import urllib.error
import urllib.request


class Handler(BaseHTTPRequestHandler):
    def forward(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        headers = {key: value for key, value in self.headers.items()
                   if key.lower() not in ('host', 'connection', 'content-length')}
        request = urllib.request.Request('https://198.51.100.10' + self.path,
                                         data=body if body else None, headers=headers, method=self.command)
        try:
            response = urllib.request.urlopen(request, context=ssl.create_default_context(cafile='/certs/server.crt'), timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            self.send_response(response.status)
            for key, value in response.headers.items():
                if key.lower() not in ('connection', 'transfer-encoding', 'content-length'):
                    self.send_header(key, value)
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = do_HEAD = forward


if __name__ == '__main__':
    ThreadingHTTPServer(('10.8.2.2', 9444), Handler).serve_forever()
