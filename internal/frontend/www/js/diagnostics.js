/**
 * Diagnostics feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const diagnosticsMethods = {
async loadDiagnostics() {
      // Sequence counter prevents stale concurrent loads from calling diagInitGrid.
      const seq = (this._diagLoadSeq = (this._diagLoadSeq || 0) + 1);
      try {
        const res = await this.api.getDashboardWidgets('diagnostics');
        const saved = res.widgets || [];
        this.diagWidgets = saved;
      } catch (e) {
        this.diagWidgets = [];
      }
      if (seq !== this._diagLoadSeq) return; // superseded by a newer load
      await this.loadGateways();
      if (seq !== this._diagLoadSeq) return;
      // Restore persisted periods and pre-load history
      for (const w of this.diagWidgets) {
        if (w.period) this.$set(this.metricsWidgetPeriod, w.id, w.period);
        const period = this.metricsWidgetPeriod[w.id] || '5m';
        if (period !== '5m') {
          for (const key of (w.graphs || [])) {
            if (key.startsWith('gateway:')) this.metricsLoadGatewayDist(w.id, key, period);
            else this.metricsLoadHistory(w.id, key, period);
          }
        }
      }
      await this.$nextTick();
      if (seq !== this._diagLoadSeq) return;
      this.diagInitGrid();
    },

diagInitGrid() {
      if (this.diagGrid) { this.diagGrid.destroy(false); this.diagGrid = null; }
      const el = document.querySelector('.diag-grid');
      if (!el) return;
      this.diagGrid = GridStack.init({
        cellHeight: 60,
        margin: 8,
        column: 12,
        columnOpts: {
          breakpointForWindow: true,
          breakpoints: [{ w: 1023, c: 1, layout: 'list' }],
        },
        animate: true,
        float: true,
        disableDrag: this.isCompactViewport,
        disableResize: this.isCompactViewport,
        draggable: { handle: '.dash-card-header' },
        resizable: { handles: 'se' },
      }, el);
      // Sync positions back to diagWidgets on change
      this.diagGrid.on('change', () => {
        if (this._diagSaveEnabled && !this.isCompactViewport) this.diagSaveLayout();
      });
      this._diagSaveEnabled = false;
      setTimeout(() => { this._diagSaveEnabled = true; }, 500);
    },

diagSaveLayout() {
      if (!this.diagGrid) return;
      const snapshot = this.diagWidgets.slice(); // stable reference against concurrent mutations
      const items = this.diagGrid.save(false);
      const widgets = (items || []).map(item => {
        const existing = snapshot.find(w => w.id === item.id);
        if (!existing) return null; // widget removed mid-save — skip
        return Object.assign({}, existing, {
          x: item.x, y: item.y, w: item.w, h: item.h,
        });
      }).filter(Boolean);
      this.diagWidgets = widgets;
      this.api.putDashboardWidgets(widgets, 'diagnostics').catch(console.error);
    },

diagAddWidget() {
      const id = 'w-monitoring-' + Date.now();
      const newW = { id, type: 'monitoring', x: 0, y: 0, w: 6, h: 5, graphs: [], title: '' };
      this.diagWidgets.push(newW);
      // Always persist immediately so the widget survives even if grid init failed.
      this.api.putDashboardWidgets(this.diagWidgets, 'diagnostics').catch(console.error);
      this.$nextTick(() => {
        if (!this.diagGrid) return;
        const el = document.querySelector(`[gs-id="${id}"]`);
        if (el) {
          this.diagGrid.makeWidget(el);
          this.diagSaveLayout();
        }
      });
    },

diagRemoveWidget(widgetId) {
      const idx = this.diagWidgets.findIndex(w => w.id === widgetId);
      if (idx === -1) return;
      if (this.diagGrid) {
        const el = document.querySelector(`[gs-id="${widgetId}"]`);
        if (el) this.diagGrid.removeWidget(el, false);
      }
      this.diagWidgets.splice(idx, 1);
      this.api.putDashboardWidgets(this.diagWidgets, 'diagnostics').catch(console.error);
    },

    // ── Ping utility ─────────────────────────────────────────────────────────

async pingLoadInterfaces() {
      if (this.pingRouterIfaces.length) return;
      try {
        const res = await this.api.getSystemInterfaces();
        this.pingRouterIfaces = res.interfaces || [];
      } catch (e) { /* silent */ }
    },

pingStart() {
      if (!this.pingHost.trim()) return;
      if (this.pingEventSource) { this.pingEventSource.close(); this.pingEventSource = null; }
      this.terminalLines = [];
      this.pingRunning = true;

      const params = new URLSearchParams({ host: this.pingHost.trim(), count: this.pingCount });
      if (this.pingSource) {
        // Resolve interface name to its first IPv4 address (Cisco-style source IP routing).
        const iface = this.pingRouterIfaces.find(i => i.name === this.pingSource);
        const sourceIp = iface && iface.addrs && iface.addrs.length ? iface.addrs[0] : this.pingSource;
        params.set('source', sourceIp);
      }
      if (this.pingSize !== '') params.set('size', this.pingSize);
      if (this.pingDf) params.set('df', 'true');
      if (this.pingTos !== '') params.set('tos', this.pingTos);

      const segs = window.location.pathname.split('/').filter(Boolean);
      const apiBase = segs.length > 0
        ? `${window.location.origin}/${segs[0]}/api`
        : `${window.location.origin}/api`;
      const url = `${apiBase}/diagnostics/ping/stream?` + params.toString();
      const es = new EventSource(url);
      this.pingEventSource = es;

      es.onmessage = (e) => {
        if (e.data === '[done]') {
          this.pingRunning = false;
          es.close();
          this.pingEventSource = null;
          return;
        }
        this.terminalLines.push(e.data);
        this.$nextTick(() => {
          const el = this.$el && this.$el.querySelector('.ping-terminal');
          if (el) el.scrollTop = el.scrollHeight;
        });
      };
      es.onerror = () => {
        this.pingRunning = false;
        es.close();
        this.pingEventSource = null;
      };
    },

pingStop() {
      if (this.pingEventSource) { this.pingEventSource.close(); this.pingEventSource = null; }
      this.pingRunning = false;
      if (this.terminalLines.length) this.terminalLines.push('--- interrupted ---');
    },

pingClear() {
      this.terminalLines = [];
    },

traceStart() {
      if (!this.traceHost.trim()) return;
      if (this.traceEventSource) { this.traceEventSource.close(); this.traceEventSource = null; }
      this.terminalLines = [];
      this.traceRunning = true;

      const params = new URLSearchParams({ host: this.traceHost.trim(), type: this.traceType });
      if (this.traceSource) {
        const iface = this.pingRouterIfaces.find(i => i.name === this.traceSource);
        const sourceIp = iface && iface.addrs && iface.addrs.length ? iface.addrs[0] : this.traceSource;
        params.set('source', sourceIp);
      }

      const segs = window.location.pathname.split('/').filter(Boolean);
      const apiBase = segs.length > 0
        ? `${window.location.origin}/${segs[0]}/api`
        : `${window.location.origin}/api`;
      const url = `${apiBase}/diagnostics/traceroute/stream?` + params.toString();
      const es = new EventSource(url);
      this.traceEventSource = es;

      es.onmessage = (e) => {
        if (e.data === '[done]') {
          this.traceRunning = false;
          es.close();
          this.traceEventSource = null;
          return;
        }
        this.terminalLines.push(e.data);
        this.$nextTick(() => {
          const el = this.$el && this.$el.querySelector('.ping-terminal');
          if (el) el.scrollTop = el.scrollHeight;
        });
      };
      es.onerror = () => {
        this.traceRunning = false;
        es.close();
        this.traceEventSource = null;
      };
    },

traceStop() {
      if (this.traceEventSource) { this.traceEventSource.close(); this.traceEventSource = null; }
      this.traceRunning = false;
      if (this.terminalLines.length) this.terminalLines.push('--- interrupted ---');
    },

tcpdumpStart() {
      if (!this.tcpdumpIface) return;
      if (this.tcpdumpEventSource) { this.tcpdumpEventSource.close(); this.tcpdumpEventSource = null; }
      this.terminalLines = [];
      this.tcpdumpPcapId = null;
      this.tcpdumpDownloadReady = false;
      this.tcpdumpRunning = true;

      const params = new URLSearchParams({ iface: this.tcpdumpIface });
      if (this.tcpdumpFilter.trim()) params.set('filter', this.tcpdumpFilter.trim());
      if (this.tcpdumpSave) params.set('save', 'true');

      const segs = window.location.pathname.split('/').filter(Boolean);
      const apiBase = segs.length > 0
        ? `${window.location.origin}/${segs[0]}/api`
        : `${window.location.origin}/api`;
      this._tcpdumpApiBase = apiBase;
      const url = `${apiBase}/diagnostics/tcpdump/stream?` + params.toString();
      const es = new EventSource(url);
      this.tcpdumpEventSource = es;

      es.onmessage = (e) => {
        if (e.data === '[done]') {
          this.tcpdumpRunning = false;
          es.close();
          this.tcpdumpEventSource = null;
          // If a capture ID was received, file is now ready to download.
          if (this.tcpdumpPcapId) this.tcpdumpDownloadReady = true;
          return;
        }
        // Backend sends capture ID as first event when save=true.
        if (e.data.startsWith('[captureid:') && e.data.endsWith(']')) {
          this.tcpdumpPcapId = e.data.slice(11, -1);
          return; // don't print to terminal
        }
        this.terminalLines.push(e.data);
        this.$nextTick(() => {
          const el = this.$el && this.$el.querySelector('.ping-terminal');
          if (el) el.scrollTop = el.scrollHeight;
        });
      };
      es.onerror = () => {
        this.tcpdumpRunning = false;
        es.close();
        this.tcpdumpEventSource = null;
      };
    },

tcpdumpStop() {
      if (this.tcpdumpEventSource) { this.tcpdumpEventSource.close(); this.tcpdumpEventSource = null; }
      this.tcpdumpRunning = false;
      if (this.terminalLines.length) this.terminalLines.push('--- stopping, finalizing file…');

      if (this.tcpdumpSave && this.tcpdumpPcapId) {
        // Ask backend to SIGINT tcpdump so it flushes and closes the pcap file.
        const apiBase = this._tcpdumpApiBase || (window.location.origin + '/api');
        fetch(`${apiBase}/diagnostics/tcpdump/stop?file=` + encodeURIComponent(this.tcpdumpPcapId), { method: 'POST' })
          .then(() => { this.tcpdumpDownloadReady = true; })
          .catch(() => { this.tcpdumpDownloadReady = true; }); // show button anyway
      } else {
        if (this.terminalLines.length) this.terminalLines[this.terminalLines.length - 1] = '--- interrupted ---';
      }
    },

tcpdumpDownload() {
      if (!this.tcpdumpPcapId) return;
      const apiBase = this._tcpdumpApiBase || (window.location.origin + '/api');
      const url = `${apiBase}/diagnostics/tcpdump/download?file=` + encodeURIComponent(this.tcpdumpPcapId);
      const a = document.createElement('a');
      a.href = url;
      a.download = 'capture-' + this.tcpdumpPcapId.slice(0, 8) + '.pcap';
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      this.tcpdumpPcapId = null;
      this.tcpdumpDownloadReady = false;
    },

    // ── End ping utility ─────────────────────────────────────────────────────

    // Compact elapsed time: "5s", "20m", "3h", "2d"
};

