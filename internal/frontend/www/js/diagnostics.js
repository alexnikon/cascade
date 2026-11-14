/** Network diagnostic utilities; monitoring widgets belong to Dashboard. */
export const diagnosticsMethods = {
async pingLoadInterfaces() {
      try {
        const res = await this.api.getSystemInterfaces();
        this.pingRouterIfaces = res.interfaces || [];
      } catch (err) {
        this.showToast(err.message || 'Unable to load network interfaces.', 'error');
      }
    },

diagnosticsURL(path) {
      const segs = window.location.pathname.split('/').filter(Boolean);
      const prefix = segs.length ? `/${segs[0]}` : '';
      return `${window.location.origin}${prefix}/api/diagnostics/${path}`;
    },

appendDiagnosticLine(line) {
      this.terminalLines.push(line);
      if (this.terminalLines.length > 2000) this.terminalLines.splice(0, this.terminalLines.length - 2000);
      this.$nextTick(() => {
        const el = this.$el && this.$el.querySelector('.ping-terminal');
        if (el) el.scrollTop = el.scrollHeight;
      });
    },

startDiagnosticStream(kind, params) {
      const sourceKey = `${kind}EventSource`;
      if (this[sourceKey]) this[sourceKey].close();
      this.terminalLines = [];
      this[`${kind}Running`] = true;
      const route = kind === 'trace' ? 'traceroute' : kind;
      const es = new EventSource(this.diagnosticsURL(`${route}/stream?${params}`));
      this[sourceKey] = es;
      es.onmessage = event => {
        if (this[sourceKey] !== es) return;
        if (event.data === '[done]') {
          this[`${kind}Running`] = false;
          es.close();
          this[sourceKey] = null;
          return;
        }
        this.appendDiagnosticLine(event.data);
      };
      es.onerror = () => {
        if (this[sourceKey] !== es) return;
        this[`${kind}Running`] = false;
        es.close();
        this[sourceKey] = null;
        this.appendDiagnosticLine('error: diagnostic stream disconnected.');
      };
    },

pingStart() {
      if (!this.pingHost.trim() || this.pingRunning) return;
      const params = new URLSearchParams({ host: this.pingHost.trim(), count: this.pingCount });
      if (this.pingSource) params.set('source', this.pingSource);
      if (this.pingSize !== '') params.set('size', this.pingSize);
      if (this.pingDf) params.set('df', 'true');
      if (this.pingTos !== '') params.set('tos', this.pingTos);
      this.startDiagnosticStream('ping', params);
    },

stopDiagnosticStream(kind) {
      const key = `${kind}EventSource`;
      if (this[key]) this[key].close();
      this[key] = null;
      this[`${kind}Running`] = false;
    },

pingStop() {
      this.stopDiagnosticStream('ping');
      this.appendDiagnosticLine('--- interrupted ---');
    },

pingClear() { this.terminalLines = []; },

traceStart() {
      if (!this.traceHost.trim() || this.traceRunning) return;
      const params = new URLSearchParams({ host: this.traceHost.trim(), type: this.traceType });
      if (this.traceSource) params.set('source', this.traceSource);
      this.startDiagnosticStream('trace', params);
    },

traceStop() {
      this.stopDiagnosticStream('trace');
      this.appendDiagnosticLine('--- interrupted ---');
    },

stopDiagnostics() {
      for (const kind of ['ping', 'trace']) this.stopDiagnosticStream(kind);
    },
};
