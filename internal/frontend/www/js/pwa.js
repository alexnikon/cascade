(() => {
  'use strict';

  if (!('serviceWorker' in navigator) || !window.isSecureContext) return;

  window.addEventListener('load', () => {
    // Replacement workers activate naturally after controlled tabs close.
    // Keep open forms intact and reserve release notices for the release API.
    const serviceWorkerURL = new URL('./sw.js', window.location.href);
    navigator.serviceWorker.register(serviceWorkerURL.pathname)
      .catch((error) => {
        console.warn('[Cascade PWA] Service Worker registration failed', error);
      });
  });
})();
