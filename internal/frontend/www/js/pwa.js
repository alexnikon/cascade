(() => {
  'use strict';

  if (!('serviceWorker' in navigator) || !window.isSecureContext) return;

  let reloadAfterActivation = false;
  let updateNotice;

  function showUpdateNotice(registration) {
    if (updateNotice || !navigator.serviceWorker.controller) return;

    updateNotice = document.createElement('div');
    updateNotice.id = 'cascade-pwa-update';
    updateNotice.setAttribute('role', 'status');
    updateNotice.style.cssText = [
      'position:fixed',
      'top:calc(16px + env(safe-area-inset-top))',
      'right:16px',
      'z-index:10000',
      'display:flex',
      'align-items:center',
      'gap:12px',
      'max-width:calc(100vw - 32px)',
      'padding:12px 14px',
      'border-radius:8px',
      'background:rgba(30,41,59,.96)',
      'color:#fff',
      'font:14px/1.4 system-ui,sans-serif',
      'box-shadow:0 4px 16px rgba(0,0,0,.35)',
    ].join(';');

    const message = document.createElement('span');
    message.textContent = 'A new Cascade version is available.';
    const reload = document.createElement('button');
    reload.type = 'button';
    reload.textContent = 'Reload';
    reload.style.cssText = [
      'border:0',
      'border-radius:6px',
      'padding:6px 10px',
      'background:#38bdf8',
      'color:#082f49',
      'font:600 14px system-ui,sans-serif',
      'cursor:pointer',
      'white-space:nowrap',
    ].join(';');
    reload.addEventListener('click', () => {
      reloadAfterActivation = true;
      reload.disabled = true;
      reload.textContent = 'Reloading…';
      if (registration.waiting) {
        registration.waiting.postMessage({ type: 'SKIP_WAITING' });
      } else {
        window.location.reload();
      }
    });

    updateNotice.append(message, reload);
    document.body.appendChild(updateNotice);
  }

  function watchForUpdates(registration) {
    if (registration.waiting) showUpdateNotice(registration);

    registration.addEventListener('updatefound', () => {
      const installing = registration.installing;
      if (!installing) return;
      installing.addEventListener('statechange', () => {
        if (installing.state === 'installed' && navigator.serviceWorker.controller) {
          showUpdateNotice(registration);
        }
      });
    });
  }

  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (reloadAfterActivation) {
      reloadAfterActivation = false;
      window.location.reload();
    }
  });

  window.addEventListener('load', () => {
    // The application is served from /, so /sw.js receives the full app scope.
    const serviceWorkerURL = new URL('./sw.js', window.location.href);
    navigator.serviceWorker.register(serviceWorkerURL.pathname)
      .then(watchForUpdates)
      .catch((error) => {
        console.warn('[Cascade PWA] Service Worker registration failed', error);
      });
  });
})();
