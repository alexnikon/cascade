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

handlePrefersChange(event) {
      this.systemDark = event.matches;
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
      this.isCompactViewport = event.matches;
      if (!event.matches) this.closeMobileNav(false);
      if (this.activePage === 'dashboard') {
        this.loadDashboard();
        return;
      }
      if (this.dashGrid) {
        this.dashGrid.enableMove(!event.matches);
        this.dashGrid.enableResize(!event.matches);
      }
    },

handleGlobalKeydown(event) {
      if (event.key === 'Escape' && this.mobileNavOpen) this.closeMobileNav();
    },

switchPage(pageId) {
      // FIX: destroy GridStack when leaving dashboard — clears ResizeObserver,
      // inline styles and CSS vars it set on parent elements (which caused a
      // vertical gap on the interfaces page after visiting dashboard).
      if (this.activePage === 'diagnostics' && pageId !== 'diagnostics') this.stopDiagnostics();
      if (this.activePage === 'dashboard' && pageId !== 'dashboard') {
        this._dashboardLoadGeneration = (this._dashboardLoadGeneration || 0) + 1;
        this.metricsStopPoller();
      }
      if (this.activePage === 'dashboard' && pageId !== 'dashboard' && this.dashGrid) {
        // destroy(false) — remove GridStack listeners/styles but keep DOM nodes
        // so that Vue's v-if can cleanly remove the subtree without conflict.
        this.dashDestroyGrid();
        // Clear GridStack-set inline styles from the grid container and items after destroy.
        document.querySelectorAll('#dashboard-grid, #dashboard-grid .grid-stack-item-content, #dashboard-grid .grid-stack-item').forEach(el => { el.style.cssText = ''; });
      }
      if (this._dashResizeObs) {
        this._dashResizeObs.disconnect();
        this._dashResizeObs = null;
      }
      this.pageReadErrors = {};
      this.pageReadGeneration = (this.pageReadGeneration || 0) + 1;
      this.activePage = pageId === 'administration' ? 'dashboard' : pageId;
      if (this.isCompactViewport) this.closeMobileNav(false);
      // Reset scroll and any GridStack inline styles AFTER Vue updates the DOM.
      this.$nextTick(() => {
        // Reset all inline styles GridStack may have set on the scroll container,
        // then restore only the original padding. height/overflow come from CSS.
        const mainEl = document.querySelector('.app-main');
        if (mainEl) {
          mainEl.scrollTop = 0;
        }
        const content = document.querySelector('.app-main-content');
        if (content) content.scrollTop = 0;
        const dashboardScroll = document.querySelector('.dashboard-scroll');
        if (dashboardScroll) {
          dashboardScroll.scrollTop = 0;
        }
        // Also reset window scroll in case overflow:hidden on mainEl caused
        // the window to be the scroll container while on the previous page.
        window.scrollTo(0, 0);
      });
      this.loadPageData(this.activePage);
    },

retryPageData() {
      if (this.activePage === 'dashboard') {
        this.metricsStopPoller();
        if (this.pageReadErrors['/aliases/client-groups']) this.loadClientGroups();
      }
      this.loadPageData(this.activePage);
    },

loadPageData(pageId) {
      if (pageId === 'dashboard') {
        // loadDashboard already calls dashInitGrid after await $nextTick —
        // do not call it separately here to avoid a double-init race.
        this.loadDashboard();
        this.loadSystemInfo();
        this.metricsStartPoller();
      }
      if (pageId === 'diagnostics') {
        this.pingLoadInterfaces();
      }
      if (pageId === 'interfaces') {
        this.loadTunnelInterfaces();
        if (this.activeInterfaceId) this.refreshPeers();
        else this.refreshAllPeers();
      }
      if (pageId === 'settings') { this.loadActiveSessions(); this.loadSettings(); this.loadMetricsSettings(); this.loadDomainAliasDNS(); this.loadUsers(); this.loadApiTokens(); this.loadPreRestoreBackups(); }
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
