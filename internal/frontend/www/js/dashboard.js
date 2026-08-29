/**
 * Dashboard feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const dashboardMethods = {
async loadDashboard() {
      try {
        const res = await this.api.getDashboardWidgets();
        const saved = res.widgets || [];
        this.dashWidgets = saved.length > 0 ? saved : this.dashDefaultWidgets();
      } catch (e) {
        this.dashWidgets = this.dashDefaultWidgets();
      }
      // Pre-load NAT + aliases data (aliases needed to resolve sourceAliasId names)
      if (this.natRules.length === 0) this.loadNatRules();
      if (this.dnatRules.length === 0) this.loadDnatRules();
      if (this.aliases.length === 0) this.loadAliases();
      // Restore saved periods and load history for monitoring widgets
      for (const w of this.dashWidgets) {
        if (w.type !== 'monitoring') continue;
        // Restore persisted period (saved in w.period field)
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
      this.dashInitGrid();
    },

dashInitGrid() {
      if (this.dashGrid) {
        this.dashGrid.destroy(false);
        this.dashGrid = null;
      }
      const el = document.getElementById('dashboard-grid');
      if (!el) return;

      // Responsive GridStack teardown can leave gs-1 classes and compact DOM
      // attributes behind. Rehydrate every item from the persisted Vue model
      // before init so a mobile-first session restores the desktop layout.
      el.className = 'grid-stack';
      el.style.cssText = '';
      for (const widget of this.dashWidgets) {
        const item = el.querySelector(`[gs-id="${widget.id}"]`);
        if (!item) continue;
        item.className = 'grid-stack-item';
        item.style.cssText = '';
        item.setAttribute('gs-x', widget.x);
        item.setAttribute('gs-y', widget.y);
        item.setAttribute('gs-w', widget.w);
        item.setAttribute('gs-h', widget.h);
        const content = item.querySelector('.grid-stack-item-content');
        if (content) content.style.cssText = '';
      }

      // GridStack auto-adopts existing .grid-stack-item children rendered by Vue v-for.
      // FIX: GridStack fires 'change' synchronously during init() before all items are placed,
      // which would cause dashSaveLayout to overwrite correct positions with wrong ones.
      // We use a flag to suppress saves during init and enable them after a short delay.
      this._dashSaveEnabled = false;
      const grid = GridStack.init({
        column: 12,
        columnOpts: {
          breakpointForWindow: true,
          breakpoints: [{ w: 1023, c: 1, layout: 'list' }],
        },
        cellHeight: 60,
        margin: 8,
        animate: true,
        float: true,
        disableDrag: this.isCompactViewport,
        disableResize: this.isCompactViewport,
        draggable: { handle: '.dash-card-header' },
        resizable: { handles: 'se' },
      }, el);

      grid.on('change', () => {
        if (this._dashSaveEnabled && !this.isCompactViewport) this.dashSaveLayout(grid);
      });
      this.dashGrid = grid;

      // Enable saving after GridStack's init-time change events have all fired
      setTimeout(() => {
        this._dashSaveEnabled = true;
        this.dashInitResizeObservers();
      }, 300);

      if (this.dashWidgets.some(w => w.type === 'server-info')) {
        this.loadSystemInfo();
      }

      // Restore CSS zoom for non-monitoring widgets that have a saved fontScale.
      this.$nextTick(() => {
        this.dashWidgets.forEach(w => {
          if (w.type === 'monitoring' || !w.fontScale || w.fontScale === 1.0) return;
          const gsItem = document.querySelector(`[gs-id="${w.id}"]`);
          if (!gsItem) return;
          const card = gsItem.querySelector('.dash-card');
          if (card) card.style.zoom = w.fontScale;
        });
      });
    },

    // Attach ResizeObserver to each widget content container.
    // Applies CSS zoom to the inner .dash-card so fonts/icons/buttons scale
    // automatically when the widget is resized. GridStack layout is unaffected
    // because it measures the outer .grid-stack-item-content, not .dash-card.

dashInitResizeObservers() {
      if (this._dashResizeObs) {
        this._dashResizeObs.disconnect();
        this._dashResizeObs = null;
      }
      const grid = document.getElementById('dashboard-grid');
      if (!grid) return;

      // Proportional scaling: 450px = 1.0 (reference), linear, clamped 0.55–1.5.
      // Multiplied by per-widget fontScale (user +/- adjustment, default 1.0).
      const zoomFor = (width, fontScale) =>
        Math.max(0.55, Math.min(1.5, (width / 450) * (fontScale || 1.0)));

      const obs = new ResizeObserver(entries => {
        for (const entry of entries) {
          const gsItem = entry.target.closest('[gs-id]');
          const widgetId = gsItem ? gsItem.getAttribute('gs-id') : null;
          const widget = widgetId ? this.dashWidgets.find(w => w.id === widgetId) : null;
          // CSS zoom on dash-card breaks ApexCharts tooltip coordinate mapping:
          // getBoundingClientRect() on SVG <g> elements does not account for ancestor
          // CSS zoom in all browsers, while event.clientX does → proportional drift.
          // Font scaling is handled via font-size on dash-card-body (fontScale setting).
        }
      });

      grid.querySelectorAll('.grid-stack-item-content').forEach(c => obs.observe(c));
      this._dashResizeObs = obs;
    },

    // Adjust per-widget font scale by delta (+0.1 or -0.1), clamp 0.5–2.0.
    // Triggers ResizeObserver re-fire by dispatching a resize on the content element.

dashAdjustFontScale(widgetId, delta) {
      const idx = this.dashWidgets.findIndex(w => w.id === widgetId);
      if (idx === -1) return;
      const w = this.dashWidgets[idx];
      const current = w.fontScale || 1.0;
      const next = Math.round(Math.max(0.5, Math.min(2.0, current + delta)) * 10) / 10;
      this.dashWidgets.splice(idx, 1, { ...w, fontScale: next });
      // Apply CSS zoom immediately for non-monitoring widgets.
      // monitoring widgets use interactive ApexCharts tooltips — CSS zoom breaks
      // getBoundingClientRect() coordinate mapping causing tooltip drift.
      // Other widgets (server-info, peers-summary, gateways, etc.) only contain
      // sparkline charts with tooltips disabled, so CSS zoom is safe there.
      if (w.type !== 'monitoring') {
        this.$nextTick(() => {
          const gsItem = document.querySelector(`[gs-id="${widgetId}"]`);
          if (!gsItem) return;
          const card = gsItem.querySelector('.dash-card');
          if (card) card.style.zoom = next;
        });
      }
      this.api.putDashboardWidgets(this.dashWidgets).catch(console.error);
    },

dashDefaultWidgets() {
      return [
        { id: 'w-server-info',   type: 'server-info',   x: 0, y: 0, w: 4, h: 4 },
        { id: 'w-gateways',      type: 'gateways',       x: 4, y: 0, w: 4, h: 4 },
        { id: 'w-output-traffic', type: 'monitoring',     x: 8, y: 0, w: 4, h: 4, graphs: [], title: 'Output Traffic', period: '5m' },
        { id: 'w-interfaces',    type: 'interfaces',     x: 0, y: 4, w: 6, h: 5 },
        { id: 'w-peers',         type: 'peers',          x: 6, y: 4, w: 6, h: 9 },
        { id: 'w-peers-summary', type: 'peers-summary',  x: 0, y: 9, w: 3, h: 4 },
        { id: 'w-traffic',       type: 'traffic',        x: 3, y: 9, w: 3, h: 4 },
      ];
    },

dashSaveLayout(grid) {
      const items = grid.save(false);
      const widgets = (items || []).map(item => {
        const existing = this.dashWidgets.find(w => w.id === item.id);
        // Preserve all extra fields (graphs, fontScale, etc.) alongside GridStack geometry
        return Object.assign({}, existing || {}, {
          id: item.id,
          type: existing ? existing.type : item.id.replace('w-', ''),
          x: item.x, y: item.y, w: item.w, h: item.h,
        });
      });
      this.dashWidgets = widgets;
      this.api.putDashboardWidgets(widgets).catch(console.error);
    },

dashWidgetLabel(type) {
      const t = this.dashAvailableWidgetTypes.find(x => x.type === type);
      return t ? t.label : type;
    },

dashAddWidget(type) {
      const id = 'w-' + type + '-' + Date.now();
      const newW = { id, type, x: 0, y: 0, w: 4, h: 4 };
      this.dashWidgets.push(newW);
      this.$nextTick(() => {
        if (!this.dashGrid) return;
        // Vue has rendered the new item; tell GridStack to adopt it
        const el = document.querySelector(`[gs-id="${id}"]`);
        if (el) {
          this.dashGrid.makeWidget(el);
          // Observe the new widget for resize scaling
          if (this._dashResizeObs) {
            const content = el.querySelector('.grid-stack-item-content');
            if (content) this._dashResizeObs.observe(content);
          }
        }
        if (type === 'server-info') this.loadSystemInfo();
      });
    },

dashRemoveWidget(id) {
      if (this.dashGrid) {
        const el = this.dashGrid.el
          ? this.dashGrid.el.querySelector(`[gs-id="${id}"]`)
          : null;
        // removeWidget(el, false) — removes from GridStack but keeps DOM node
        // Vue will then remove it when we splice dashWidgets
        if (el) this.dashGrid.removeWidget(el, false);
      }
      const idx = this.dashWidgets.findIndex(w => w.id === id);
      if (idx !== -1) this.dashWidgets.splice(idx, 1);
      this.api.putDashboardWidgets(this.dashWidgets).catch(console.error);
    },

dashResetLayout() {
      // Destroy grid first (keeps DOM nodes, Vue will replace them)
      if (this.dashGrid) {
        this.dashGrid.destroy(false);
        this.dashGrid = null;
      }
      const defaults = this.dashDefaultWidgets();
      this.dashWidgets = defaults;
      this.api.putDashboardWidgets(defaults).catch(console.error);
      // Wait for Vue to render the new items, then init GridStack
      this.$nextTick(() => this.dashInitGrid());
    },

async loadSystemInfo() {
      if (this.systemInfoPromise) return this.systemInfoPromise;
      const promise = (async () => {
      try {
        this.dashSystemInfo = await this.api.getSystemInfo();
      } catch (e) { /* non-fatal */ }
      })();
      this.systemInfoPromise = promise;
      try {
        return await promise;
      } finally {
        if (this.systemInfoPromise === promise) this.systemInfoPromise = null;
      }
    },

    // ── Monitoring widget methods ──────────────────────────────────────────────

metricsStartPoller() {
      if (document.hidden || this.metricsPoller) return;
      this.metricsPoller = setInterval(() => this.metricsTick(), 5000);
      this.metricsTick();
      if (this._metricsHistoryPoller) clearInterval(this._metricsHistoryPoller);
      this._metricsRefreshHistory();
      this._metricsHistoryPoller = setInterval(() => this._metricsRefreshHistory(), 30000);
      // Pause poller when tab is hidden, resume (with immediate tick) when visible again
      if (!this._metricsVisibilityHandler) {
        this._metricsVisibilityHandler = () => {
          if (document.hidden) {
            this.metricsStopPoller();
          } else {
            this.metricsStartPoller();
          }
        };
        document.addEventListener('visibilitychange', this._metricsVisibilityHandler);
      }
    },

metricsStopPoller() {
      if (this.metricsPoller) {
        clearInterval(this.metricsPoller);
        this.metricsPoller = null;
      }
      if (this._metricsHistoryPoller) {
        clearInterval(this._metricsHistoryPoller);
        this._metricsHistoryPoller = null;
      }
    },

async _metricsRefreshHistory() {
      if (this.metricsConfigWidget || document.hidden) return;
      if (this.metricsHistoryPromise) return this.metricsHistoryPromise;
      this.metricsHistoryPromise = (async () => {
        let widgets = [];
        if (this.activePage === 'dashboard') {
          widgets = this.dashWidgets.filter(w => w.type === 'monitoring');
        } else if (this.activePage === 'diagnostics') {
          widgets = this.diagWidgets.filter(w => w.type === 'monitoring');
        }
        for (const w of widgets) {
          const period = this.metricsWidgetPeriod[w.id] || '5m';
          if (period === '5m') continue;
          const requests = (w.graphs || []).map(key => {
            if (key.startsWith('gateway:')) {
              return this.metricsLoadGatewayDist(w.id, key, period);
            }
            return this.metricsLoadHistory(w.id, key, period);
          });
          await Promise.all(requests);
        }
      })();
      try {
        return await this.metricsHistoryPromise;
      } finally {
        this.metricsHistoryPromise = null;
      }
    },

async metricsTick() {
      if (this.metricsTickPromise) return this.metricsTickPromise;
      const promise = this._metricsTickNow();
      this.metricsTickPromise = promise;
      try {
        return await promise;
      } finally {
        if (this.metricsTickPromise === promise) this.metricsTickPromise = null;
      }
    },

async _metricsTickNow() {
      if (this.metricsConfigWidget) return;
      try {
        const snap = await this.api.getMetrics();
        if (snap) snap.frontend = { resourcePollSkipped: this.resourcePollSkipped };
        this.metricsSnapshot = snap;

        // Build available keys list; rebuild static keys on first snapshot,
        // but always sync gateway keys so newly added/removed gateways appear.
        if (snap) {
          if (!this.metricsAvailableKeys.length) {
            const keys = ['cpu', 'mem'];
            for (const iface of (snap.interfaces || [])) {
              keys.push(`net:${iface}:rx`);
              keys.push(`net:${iface}:tx`);
            }
            this.metricsAvailableKeys = keys;
          }
          // Gateway keys: add new, remove stale on every tick
          const gwIds = new Set(Object.keys(snap.gateways || {}));
          const gwKeys = [...gwIds].map(id => `gateway:${id}`);
          const withoutGw = this.metricsAvailableKeys.filter(k => !k.startsWith('gateway:'));
          this.metricsAvailableKeys = [...withoutGw, ...gwKeys];
          this.metricsPruneUnavailableGraphs(snap);
        }

        // Append to rolling buffers for realtime (5m) widgets — both dashboard and diagnostics
        const now = Date.now();
        const MAX_POINTS = 60;
        const allMonitoringWidgets = [
          ...this.dashWidgets.filter(w => w.type === 'monitoring'),
          ...this.diagWidgets.filter(w => w.type === 'monitoring'),
        ];
        for (const w of allMonitoringWidgets) {
          const period = this.metricsWidgetPeriod[w.id] || '5m';
          if (period !== '5m') continue;
          for (const key of (w.graphs || [])) {
            const val = this.metricsValueFromSnap(snap, key);
            if (val === null) continue;
            const bufKey = `${w.id}:${key}`;
            let buf = this.metricsHistory[bufKey];
            if (!buf) { buf = []; this.$set(this.metricsHistory, bufKey, buf); }
            buf.push({ x: now, y: Math.round(val * 100) / 100 });
            if (buf.length > MAX_POINTS) buf.shift();
            if (key.startsWith('gateway:')) this._updateGatewaySeriesCache(w.id, key);
            else this._updateAreaSeriesCache(w.id, key);
          }
        }
      } catch (e) { /* non-fatal */ }
    },

metricsPruneUnavailableGraphs(snap) {
      const valid = new Set(['cpu', 'mem']);
      for (const iface of (snap.interfaces || [])) {
        valid.add(`net:${iface}:rx`);
        valid.add(`net:${iface}:tx`);
      }
      if (this.gatewaysLoaded) {
        for (const gateway of this.gateways) valid.add(`gateway:${gateway.id}`);
      } else {
        for (const id of Object.keys(snap.gateways || {})) valid.add(`gateway:${id}`);
      }

      const prunePage = (widgets, page) => {
        let changed = false;
        const next = widgets.map(widget => {
          if (widget.type !== 'monitoring') return widget;
          const currentGraphs = widget.graphs || [];
          const graphs = currentGraphs.filter(key => valid.has(key));
          if (graphs.length === currentGraphs.length) return widget;
          changed = true;
          for (const key of currentGraphs.filter(key => !valid.has(key))) {
            const cacheKey = `${widget.id}:${key}`;
            this.$delete(this.metricsHistory, cacheKey);
            this.$delete(this.metricsGatewayDist, cacheKey);
            this.$delete(this.metricsGatewaySeriesCache, cacheKey);
            this.$delete(this.metricsAreaSeriesCache, cacheKey);
            if (this._chartOptionsCache) {
              const prefix = `${widget.id}:${key}:`;
              Object.keys(this._chartOptionsCache).forEach(k => {
                if (k.startsWith(prefix)) delete this._chartOptionsCache[k];
              });
            }
          }
          return { ...widget, graphs };
        });
        if (!changed) return widgets;
        this.api.putDashboardWidgets(next, page).catch(console.error);
        return next;
      };

      this.dashWidgets = prunePage(this.dashWidgets, 'dashboard');
      this.diagWidgets = prunePage(this.diagWidgets, 'diagnostics');
    },

metricsValueFromSnap(snap, key) {
      if (!snap) return null;
      if (key === 'cpu') return snap.cpu ?? null;
      if (key === 'mem') return snap.mem ?? null;
      if (key.startsWith('net:')) {
        const [, iface, dir] = key.split(':');
        return snap.net?.[iface]?.[dir === 'rx' ? 'rxMbps' : 'txMbps'] ?? null;
      }
      if (key.startsWith('gateway:')) {
        const id = key.slice(8);
        return snap.gateways?.[id] ?? null;
      }
      return null;
    },

metricsKeyLabel(key) {
      if (key === 'cpu') return 'CPU %';
      if (key === 'mem') return 'RAM %';
      if (key.startsWith('net:')) {
        const [, iface, dir] = key.split(':');
        const ti = (this.tunnelInterfaces || []).find(t => t.id === iface);
        const label = ti && ti.name ? `${ti.name} (${iface})` : iface;
        return `${label} ${dir.toUpperCase()} Mbps`;
      }
      if (key.startsWith('gateway:')) {
        const id = key.slice(8);
        const gw = (this.gateways || []).find(g => g.id === id);
        return gw ? gw.name : id;
      }
      return key;
    },

    // Returns fill color for a gateway status value (3=healthy, 2=degraded, 1=down, 0=admin_down)

_gatewayStatusColor(val) {
      const v = Math.round(val);
      if (v >= 3) return '#22c55e'; // healthy — green
      if (v >= 2) return '#eab308'; // degraded — yellow
      if (v >= 1) return '#ef4444'; // down — red
      return '#9ca3af';               // admin_down / unknown — gray
    },

metricsYAxisTitle(key) {
      if (key === 'cpu' || key === 'mem') return '%';
      return 'Mbps';
    },

async metricsLoadHistory(widgetId, key, period) {
      try {
        const res = await this.api.getMetricsHistory({ key, period });
        const points = (res.points || []).map(p => ({ x: p[0], y: Math.round(p[1] * 100) / 100 }));
        this.$set(this.metricsHistory, `${widgetId}:${key}`, points);
        this._updateAreaSeriesCache(widgetId, key);
      } catch (e) { /* non-fatal */ }
    },

async metricsLoadGatewayDist(widgetId, key, period) {
      try {
        const res = await this.api.getMetricsGatewayDist({ key, period });
        this.$set(this.metricsGatewayDist, `${widgetId}:${key}`, res.buckets || []);
        this._updateGatewaySeriesCache(widgetId, key);
      } catch (e) { /* non-fatal */ }
    },

metricsGatewayTooltipFormatter(widgetId) {
      const period = this.metricsWidgetPeriod[widgetId] || '5m';
      if (period === '5m') {
        // Each tick is exactly one status — show name, hide zero series
        return function(v, opts) {
          if (!v) return undefined; // hide zero-value series from tooltip
          return opts.w.config.series[opts.seriesIndex].name;
        };
      }
      return function(v) { return v + '%'; };
    },

    // Returns cached series for gateway bar chart. Stable reference → ApexCharts skips re-render
    // when data hasn't changed. Cache is updated only in _updateGatewaySeriesCache.

metricsGetGatewayStackedSeries(widgetId, key) {
      return this.metricsGatewaySeriesCache[`${widgetId}:${key}`] || [];
    },

    // Builds 4 stacked series and stores in cache. Call whenever underlying data changes.

_updateGatewaySeriesCache(widgetId, key) {
      const period = this.metricsWidgetPeriod[widgetId] || '5m';
      const healthy   = { name: 'Healthy',    data: [] };
      const degraded  = { name: 'Degraded',   data: [] };
      const down      = { name: 'Down',       data: [] };
      const adminDown = { name: 'Admin Down', data: [] };

      if (period === '5m') {
        const buf = this.metricsHistory[`${widgetId}:${key}`] || [];
        for (const p of buf) {
          const v = Math.round(p.y);
          healthy.data.push(  { x: p.x, y: v >= 3 ? 1 : 0 });
          degraded.data.push( { x: p.x, y: v === 2 ? 1 : 0 });
          down.data.push(     { x: p.x, y: v === 1 ? 1 : 0 });
          adminDown.data.push({ x: p.x, y: v <= 0 ? 1 : 0 });
        }
      } else {
        const dist = this.metricsGatewayDist[`${widgetId}:${key}`] || [];
        for (const b of dist) {
          const [ts, h, d, dn, ad] = b;
          const total = h + d + dn + ad || 1;
          healthy.data.push(  { x: ts, y: Math.round(h  / total * 100) });
          degraded.data.push( { x: ts, y: Math.round(d  / total * 100) });
          down.data.push(     { x: ts, y: Math.round(dn / total * 100) });
          adminDown.data.push({ x: ts, y: Math.round(ad / total * 100) });
        }
      }
      this.$set(this.metricsGatewaySeriesCache, `${widgetId}:${key}`, [adminDown, down, degraded, healthy]);
    },

async metricsOnPeriodChange(widgetId, period) {
      this.$set(this.metricsWidgetPeriod, widgetId, period);
      const isDiag = this.activePage === 'diagnostics';
      const list = isDiag ? this.diagWidgets : this.dashWidgets;
      const idx = list.findIndex(w => w.id === widgetId);
      if (idx === -1) return;
      // Persist period into widget layout so it survives page refresh.
      // Debounced: rapid switching won't fire multiple concurrent PUT requests.
      const updated = { ...list[idx], period };
      list.splice(idx, 1, updated);
      const page = isDiag ? 'diagnostics' : 'dashboard';
      if (this._metricsPeriodSaveTimer) clearTimeout(this._metricsPeriodSaveTimer);
      this._metricsPeriodSaveTimer = setTimeout(() => {
        this.api.putDashboardWidgets(isDiag ? this.diagWidgets : this.dashWidgets, page).catch(console.error);
      }, 500);
      const w = updated;
      // Clear existing buffer so chart doesn't mix realtime and history points
      for (const key of (w.graphs || [])) {
        this.$set(this.metricsHistory, `${widgetId}:${key}`, []);
        this.$set(this.metricsGatewayDist, `${widgetId}:${key}`, []);
        this.$set(this.metricsGatewaySeriesCache, `${widgetId}:${key}`, []);
        this.$set(this.metricsAreaSeriesCache, `${widgetId}:${key}`, []);
        if (this._chartOptionsCache) {
          const prefix = `${widgetId}:${key}:`;
          Object.keys(this._chartOptionsCache).forEach(k => { if (k.startsWith(prefix)) delete this._chartOptionsCache[k]; });
        }
      }
      if (period === '5m') return; // realtime — poller fills it from next tick
      for (const key of (w.graphs || [])) {
        if (key.startsWith('gateway:')) {
          await this.metricsLoadGatewayDist(widgetId, key, period);
        } else {
          await this.metricsLoadHistory(widgetId, key, period);
        }
      }
    },

metricsGetSeries(widgetId, key) {
      const data = this.metricsHistory[`${widgetId}:${key}`] || [];
      if (!key.startsWith('gateway:')) return data;
      return data.map(p => ({ x: p.x, y: 1 }));
    },

    // Builds series wrapper for area charts; call whenever underlying data changes.
    // Always replaces with $set so ApexCharts detects the change and re-renders.
    // Chart options are stable via _chartOptionsCache, so this does not cause re-init.

_updateAreaSeriesCache(widgetId, key) {
      const cacheKey = `${widgetId}:${key}`;
      const buf = this.metricsHistory[cacheKey] || [];
      this.$set(this.metricsAreaSeriesCache, cacheKey, [
        { name: this.metricsKeyLabel(key), data: buf.slice() },
      ]);
    },

metricsGetCachedAreaSeries(widgetId, key) {
      return this.metricsAreaSeriesCache[`${widgetId}:${key}`] || [];
    },

    // Returns stable chart options object for area charts.
    // Keyed by all fields that affect the options so changes (theme, period, color) get fresh object.

metricsAreaChartOptions(widgetId, key) {
      const w = [...this.dashWidgets, ...this.diagWidgets].find(w => w.id === widgetId);
      const period = this.metricsWidgetPeriod[widgetId] || '5m';
      const color = (w && w.graphColors && w.graphColors[key]) || '#22d3ee';
      const ck = `${widgetId}:${key}:${period}:${this.theme}:${color}`;
      if (!this._chartOptionsCache) this._chartOptionsCache = {};
      if (!this._chartOptionsCache[ck]) {
        this._chartOptionsCache[ck] = {
          chart: { id: widgetId+'_'+key, type:'area', animations:{ enabled:false }, toolbar:{ show:false }, sparkline:{ enabled:false }, background:'transparent' },
          colors: [color],
          stroke: { curve:'smooth', width:2 },
          markers: { size: 0 },
          dataLabels: { enabled: false },
          fill: { type:'gradient', gradient:{ shadeIntensity:1, opacityFrom:0.4, opacityTo:0.05 } },
          xaxis: { type:'datetime', labels:{ show: period !== '5m', datetimeUTC:false, style:{ fontSize:'9px', colors: this.theme==='dark'?'#a3a3a3':'#9ca3af' } }, axisBorder:{ show:false }, axisTicks:{ show:false }, tooltip:{ enabled:false } },
          yaxis: { labels:{ show:true, minWidth:42, maxWidth:42, style:{ fontSize:'9px', colors: this.theme==='dark'?'#a3a3a3':'#9ca3af' } }, min:0 },
          grid: { borderColor: this.theme==='dark'?'#404040':'#f0f0f0', padding:{ left:0, right:0, top:0 } },
          tooltip: { fixed:{ enabled:true, position:'topRight', offsetX:-12, offsetY:8 }, x:{ format:'HH:mm:ss' }, theme: this.theme==='dark'?'dark':'light' },
          theme: { mode: this.theme==='dark'?'dark':'light' },
        };
      }
      return this._chartOptionsCache[ck];
    },

    // Returns pre-computed per-bar color array for gateway bar charts.
    // Used with distributed:true so colors[i] maps to bar i directly — no dataPointIndex risk.

metricsGatewayColors(widgetId, key) {
      const data = this.metricsHistory[`${widgetId}:${key}`] || [];
      return data.map(p => this._gatewayStatusColor(p.y));
    },

    // Tooltip label for gateway status by index into the raw history buffer.

metricsGatewayTooltip(widgetId, key, dataPointIndex) {
      const data = this.metricsHistory[`${widgetId}:${key}`] || [];
      const p = data[dataPointIndex];
      const v = p ? Math.round(p.y) : -1;
      return ['Admin Down', 'Down', 'Degraded', 'Healthy'][v] || 'Unknown';
    },

metricsCloseConfig() {
      this.metricsConfigWidget = null;
      this.metricsTick();
    },

metricsOpenConfig(widgetId) {
      // Snapshot the page at open time so Save writes to the correct list
      // even if the user navigates away while the modal is open.
      const page = this.activePage === 'diagnostics' ? 'diagnostics' : 'dashboard';
      const list = page === 'diagnostics' ? this.diagWidgets : this.dashWidgets;
      const w = list.find(w => w.id === widgetId);
      if (!w) return;
      this.metricsConfigWidget = widgetId;
      this.metricsConfigPage = page;
      this.metricsConfigDraft = [...(w.graphs || [])];
      this.metricsColorDraft = Object.assign({}, w.graphColors || {});
      this.metricsTitleDraft = w.title || '';
    },

metricsToggleGraph(key) {
      const idx = this.metricsConfigDraft.indexOf(key);
      if (idx === -1) this.metricsConfigDraft.push(key);
      else this.metricsConfigDraft.splice(idx, 1);
    },

async metricsSaveConfig() {
      const widgetId = this.metricsConfigWidget;
      const page = this.metricsConfigPage || 'dashboard';
      const isDiag = page === 'diagnostics';
      const list = isDiag ? this.diagWidgets : this.dashWidgets;
      const idx = list.findIndex(w => w.id === widgetId);
      if (idx === -1) return;
      const title = this.metricsTitleDraft.trim() || '';
      const w = { ...list[idx], graphs: [...this.metricsConfigDraft], graphColors: { ...this.metricsColorDraft }, title };
      list.splice(idx, 1, w);
      this.metricsCloseConfig();
      this.api.putDashboardWidgets(isDiag ? this.diagWidgets : this.dashWidgets, page).catch(console.error);
      // Load history for newly added graphs if not in realtime mode
      const period = this.metricsWidgetPeriod[widgetId] || '5m';
      if (period !== '5m') {
        for (const key of w.graphs) {
          if (key.startsWith('gateway:')) await this.metricsLoadGatewayDist(widgetId, key, period);
          else await this.metricsLoadHistory(widgetId, key, period);
        }
      }
    },

    // ── Diagnostics page ─────────────────────────────────────────────────────

dashTimeShort(ts) {
      if (!ts) return '';
      const sec = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
      if (sec < 0) return '';
      if (sec < 60) return sec + 's';
      if (sec < 3600) return Math.floor(sec / 60) + 'm';
      if (sec < 86400) return Math.floor(sec / 3600) + 'h';
      return Math.floor(sec / 86400) + 'd';
    },

dashFmtBytes(bytes) {
      if (!bytes || bytes === 0) return '0 B';
      const units = ['B', 'KB', 'MB', 'GB', 'TB'];
      const i = Math.floor(Math.log(bytes) / Math.log(1024));
      return (bytes / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0) + ' ' + units[Math.min(i, units.length - 1)];
    },

    // Returns (or lazily creates) reactive per-widget peers filter state

dashPeersGetState(widgetId) {
      if (!this.dashPeersState[widgetId]) {
        const widget = this.dashWidgets.find(w => w.id === widgetId) || {};
        this.$set(this.dashPeersState, widgetId, {
          iface: widget.peerFilter || '',
          sort: widget.peerSort || 'name',
        });
      }
      return this.dashPeersState[widgetId];
    },

dashPeersViewDirty(widgetId) {
      const state = this.dashPeersGetState(widgetId);
      const widget = this.dashWidgets.find(w => w.id === widgetId) || {};
      return state.iface !== (widget.peerFilter || '') ||
        state.sort !== (widget.peerSort || 'name');
    },

async dashSavePeersView(widgetId) {
      const state = this.dashPeersGetState(widgetId);
      const idx = this.dashWidgets.findIndex(w => w.id === widgetId);
      if (idx === -1) return;
      const updated = {
        ...this.dashWidgets[idx],
        peerFilter: state.iface || '',
        peerSort: state.sort || 'name',
      };
      const widgets = [...this.dashWidgets];
      widgets.splice(idx, 1, updated);
      try {
        await this.api.putDashboardWidgets(widgets);
        this.dashWidgets.splice(idx, 1, updated);
        this.showToast('Peers view saved', 'success');
      } catch (err) {
        this.showToast(`Failed to save peers view: ${err.message}`, 'error');
      }
    },

async dashResetPeersView(widgetId) {
      this.$set(this.dashPeersState, widgetId, { iface: '', sort: 'name' });
      const idx = this.dashWidgets.findIndex(w => w.id === widgetId);
      if (idx === -1) return;
      const updated = { ...this.dashWidgets[idx] };
      delete updated.peerFilter;
      delete updated.peerSort;
      const widgets = [...this.dashWidgets];
      widgets.splice(idx, 1, updated);
      try {
        await this.api.putDashboardWidgets(widgets);
        this.dashWidgets.splice(idx, 1, updated);
        this.showToast('Peers view reset', 'success');
      } catch (err) {
        this.showToast(`Failed to reset peers view: ${err.message}`, 'error');
      }
    },

    // Returns filtered + sorted peers for a given widget

dashFilteredPeers(widgetId) {
      const s = this.dashPeersGetState(widgetId);
      let peers = this.allPeers;
      if (s.iface === '__clients__') {
        peers = peers.filter(p => p.peerType !== 'interconnect');
      } else if (s.iface === '__s2s__') {
        peers = peers.filter(p => p.peerType === 'interconnect');
      } else if (s.iface) {
        peers = peers.filter(p => p.interfaceId === s.iface);
      }
      if (s.sort === 'traffic') {
        peers = [...peers].sort((a, b) =>
          ((b.totalTx || 0) + (b.totalRx || 0)) - ((a.totalTx || 0) + (a.totalRx || 0)));
      } else if (s.sort === 'seen') {
        peers = [...peers].sort((a, b) => {
          const ta = a.latestHandshakeAt ? new Date(a.latestHandshakeAt).getTime() : 0;
          const tb = b.latestHandshakeAt ? new Date(b.latestHandshakeAt).getTime() : 0;
          return tb - ta;
        });
      } else {
        peers = [...peers].sort((a, b) => (a.name || '').localeCompare(b.name || ''));
      }
      return peers;
    },

    // Human-readable protocol label for dashboard badges

dashProtoLabel(protocol) {
      if (protocol === 'wireguard-1.0' || protocol === 'wireguard') return 'WG1.0';
      if (protocol === 'amneziawg-2.0') return 'AWG2.0';
	  if (protocol === 'amneziawg-3.1') return 'AWG3.1';
      if (protocol === 'amneziawg') return 'AWG';
      return protocol || 'WG';
    },

    // ── End Dashboard ──────────────────────────────────────────────────────────
};
