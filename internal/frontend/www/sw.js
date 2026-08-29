/* global caches, clients, fetch, URL */

'use strict';

// Bump this value when the set or behavior of cached frontend assets changes.
// The service worker script itself is served with no-cache so new workers are
// discovered without relying on a long-lived browser cache entry.
const CACHE_VERSION = 'cascade-static-v1';
const STATIC_CACHE = CACHE_VERSION;

const STATIC_PATHS = [
  '/css/',
  '/js/',
  '/img/',
  '/docs/',
];

function isDynamicPath(pathname) {
  // Cascade API and authentication/session endpoints are always network-only.
  return /^\/(?:api(?:\/|$)|auth(?:\/|$)|login(?:\/|$)|logout(?:\/|$)|session(?:\/|$))/.test(pathname);
}

function isStaticPath(pathname) {
  return pathname === '/manifest.json'
    || STATIC_PATHS.some((prefix) => pathname.startsWith(prefix));
}

async function networkFirstStatic(request) {
  const cache = await caches.open(STATIC_CACHE);
  try {
    const response = await fetch(request, { cache: 'no-cache' });
    if (response.ok) {
      try {
        await cache.put(request, response.clone());
      } catch (_) {
        // A cache write failure must never prevent the fresh response.
      }
    }
    return response;
  } catch (error) {
    // Only static assets may use this fallback. HTML and backend state never
    // reach this function, so they cannot silently become stale here.
    const cached = await cache.match(request);
    if (cached) return cached;
    throw error;
  }
}

self.addEventListener('install', () => {
  // Keep a replacement worker waiting until the user explicitly accepts the
  // update. This avoids interrupting an open administration workflow.
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(
        keys
          .filter((key) => key.startsWith('cascade-static-') && key !== STATIC_CACHE)
          .map((key) => caches.delete(key)),
      ))
      .then(() => clients.claim()),
  );
});

self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'SKIP_WAITING') {
    self.skipWaiting();
  }
});

self.addEventListener('fetch', (event) => {
  const request = event.request;
  if (request.method !== 'GET') return;

  const url = new URL(request.url);
  if (url.origin !== self.location.origin
      || request.mode === 'navigate'
      || url.pathname === '/sw.js'
      || isDynamicPath(url.pathname)
      || !isStaticPath(url.pathname)) {
    // Network-only includes API, auth/session, navigation, and third-party
    // requests. There is deliberately no cached fallback for these paths.
    return;
  }

  event.respondWith(networkFirstStatic(request));
});
