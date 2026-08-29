# Cascade PWA

Cascade's PWA support is intentionally conservative. The service worker is
served from `/sw.js`, keeps the current worker active until the user accepts an
update, and caches only same-origin static frontend assets. HTML navigation,
API requests, authentication/session requests, and backend state are always
network-only.

Service Workers require a secure context: use HTTPS in deployments, or a
browser-supported localhost secure context for local testing. No certificate
generation or permission requests are added by this feature.

`CACHE_VERSION` in `internal/frontend/www/sw.js` is manually maintained. Bump
it when the cached asset set or its compatibility contract changes. The
service worker script is served with `Cache-Control: no-cache` so browsers can
discover the replacement worker after a deployment.
