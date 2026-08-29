/**
 * Navigation feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const navigationMethods = {
toggleTheme() {
      const themes = ['light', 'dark', 'auto'];
      const currentIndex = themes.indexOf(this.uiTheme);
      this.uiTheme = themes[(currentIndex + 1) % themes.length];
      localStorage.theme = this.uiTheme;
      this.setTheme(this.uiTheme);
    },

setTheme(theme) {
      window.applyCascadeTheme(theme, this.prefersDarkScheme);
    },

handlePrefersChange() {
      if (this.uiTheme === 'auto') {
        this.setTheme('auto');
      }
    },
    // Sidebar navigation

openMobileNav() {
      if (!this.isCompactViewport) return;
      this.mobileNavOpen = true;
      this.$nextTick(() => {
        const closeButton = this.$refs.mobileNav && this.$refs.mobileNav.querySelector('.mobile-nav-close');
        if (closeButton) closeButton.focus();
      });
    },

closeMobileNav(restoreFocus = true) {
      if (!this.mobileNavOpen) return;
      this.mobileNavOpen = false;
      if (restoreFocus) {
        this.$nextTick(() => {
          if (this.$refs.mobileMenuButton) this.$refs.mobileMenuButton.focus();
        });
      }
    },

onMobileNavKeydown(event) {
      if (!this.mobileNavOpen || event.key !== 'Tab') return;
      const nav = this.$refs.mobileNav;
      if (!nav) return;
      const focusable = Array.from(nav.querySelectorAll(
        'button:not([disabled]), a[href], select:not([disabled]), input:not([disabled])'
      )).filter(el => el.offsetParent !== null);
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    },

handleCompactViewportChange(event) {
      // GridStack may emit a change while switching column modes. Suppress saves
      // before updating the viewport flag so compact geometry cannot overwrite
      // the persisted desktop layout during a mobile-to-desktop transition.
      this._dashSaveEnabled = false;
      this._diagSaveEnabled = false;
      this.isCompactViewport = event.matches;
      if (!event.matches) this.closeMobileNav(false);
      if (this.activePage === 'dashboard') {
        this.loadDashboard();
        return;
      }
      if (this.activePage === 'diagnostics') {
        this.loadDiagnostics();
        return;
      }
      [this.dashGrid, this.diagGrid].filter(Boolean).forEach(grid => {
        grid.enableMove(!event.matches);
        grid.enableResize(!event.matches);
      });
    },

handleGlobalKeydown(event) {
      if (event.key === 'Escape' && this.mobileNavOpen) this.closeMobileNav();
    },

switchPage(pageId) {
      // FIX: destroy GridStack when leaving dashboard — clears ResizeObserver,
      // inline styles and CSS vars it set on parent elements (which caused a
      // vertical gap on the interfaces page after visiting dashboard).
      if (this.activePage === 'diagnostics' && pageId !== 'diagnostics' && this.diagGrid) {
        // Clear inline styles GridStack sets on content elements and the grid container.
        // GridStack sets height on .diag-grid itself (e.g. height:60px for 1-row grid) and
        // on .grid-stack-item-content children. Without this, Vue's DOM reuse could carry
        // those heights over to unrelated elements on the next page.
        document.querySelectorAll('.diag-grid .grid-stack-item-content').forEach(el => { el.style.cssText = ''; });
        this.diagGrid.destroy(false);
        // Clear AFTER destroy too — destroy(false) may re-apply inline styles during cleanup.
        document.querySelectorAll('.diag-grid, .diag-grid .grid-stack-item-content, .diag-grid .grid-stack-item').forEach(el => { el.style.cssText = ''; });
        this.diagGrid = null;
        this.metricsStopPoller();
      }
      if (this.activePage === 'dashboard' && pageId !== 'dashboard' && this.dashGrid) {
        // destroy(false) — remove GridStack listeners/styles but keep DOM nodes
        // so that Vue's v-if can cleanly remove the subtree without conflict.
        this.dashGrid.destroy(false);
        // Clear GridStack-set inline styles from the grid container and items after destroy.
        document.querySelectorAll('#dashboard-grid, #dashboard-grid .grid-stack-item-content, #dashboard-grid .grid-stack-item').forEach(el => { el.style.cssText = ''; });
        this.dashGrid = null;
        this.metricsStopPoller();
      }
      if (this._dashResizeObs) {
        this._dashResizeObs.disconnect();
        this._dashResizeObs = null;
      }
      this.activePage = pageId;
      if (this.isCompactViewport) this.closeMobileNav(false);
      if (pageId.startsWith('wizard-')) this.wizardsExpanded = true;
      // Reset scroll and any GridStack inline styles AFTER Vue updates the DOM.
      this.$nextTick(() => {
        // Reset all inline styles GridStack may have set on the scroll container,
        // then restore only the original padding. height/overflow come from CSS.
        const mainEl = document.querySelector('.app-main');
        if (mainEl) {
          mainEl.scrollTop = 0;
        }
        const dashboardScroll = document.querySelector('.dashboard-scroll');
        if (dashboardScroll) {
          dashboardScroll.scrollTop = 0;
        }
        // Also reset window scroll in case overflow:hidden on mainEl caused
        // the window to be the scroll container while on the previous page.
        window.scrollTo(0, 0);
      });
      if (pageId === 'dashboard') {
        // loadDashboard already calls dashInitGrid after await $nextTick —
        // do not call it separately here to avoid a double-init race.
        this.loadDashboard();
        this.loadSystemInfo();
        this.metricsStartPoller();
      }
      if (pageId === 'diagnostics') {
        this.loadDiagnostics();
        this.metricsStartPoller();
        this.pingLoadInterfaces();
      }
      if (pageId === 'interfaces') this.loadTunnelInterfaces();
      if (pageId === 'settings') { this.loadSettings(); this.loadMetricsSettings(); this.loadUsers(); this.loadApiTokens(); this.loadPreRestoreBackups(); }
      if (pageId === 'remotes') this.loadRemotes();
      if (pageId === 'gateways') {
        this.loadGateways();
        this.loadGatewayGroups();
        this.loadSystemInterfaces();
      }
      if (pageId === 'routing') {
        // Run in parallel because loadKernelRoutes does not depend on the table list.
        this.loadRoutingTables();
        this.loadKernelRoutes();
        this.loadStaticRoutes();
        if (!this.gateways.length) this.loadGateways();
        if (!this.gatewayGroups.length) this.loadGatewayGroups();
        this.loadNatInterfaces();
      }
      if (pageId === 'nat') {
        this.loadNatInterfaces();
        this.loadNatRules();
        if (!this.aliases.length) this.loadAliases();
      }
      if (pageId === 'firewall-aliases') {
        this.loadAliases();
        this.loadClientGroups();
      }
      if (pageId === 'wizard-simple-vpn') {
        this.wizardVPNInit();
      }
      if (pageId === 'wizard-uplink-vpn') {
        this.wizardUplinkInit();
      }
      if (pageId === 'wizard-cascade-s2s') {
        this.wizardS2SInit();
      }
      if (pageId === 'firewall') {
        this.loadFirewallRules();
        this.loadFirewallInterfaces();
        if (!this.aliases.length) this.loadAliases();
        if (!this.gateways.length) this.loadGateways();
        if (!this.gatewayGroups.length) this.loadGatewayGroups();
      }
    },

    // ========================================================================
    // Tunnel Interfaces Methods (New Architecture)
    // ========================================================================
};
