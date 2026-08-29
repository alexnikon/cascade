/**
 * Notifications feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const notificationMethods = {
showToast(message, type = 'success', duration) {
      const id = Date.now() + Math.random();
      // Errors persist until manually dismissed; success/info auto-dismiss after 5s
      const ms = duration !== undefined ? duration : (type === 'error' ? 0 : 5000);
      this.toasts.push({ id, message, type });
      if (ms > 0) setTimeout(() => this.dismissToast(id), ms);
    },

dismissToast(id) {
      const idx = this.toasts.findIndex(t => t.id === id);
      if (idx !== -1) this.toasts.splice(idx, 1);
    },
};

