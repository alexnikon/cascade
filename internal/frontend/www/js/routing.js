/**
 * Routing feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const routingMethods = {
switchRoutingTab(tab) {
      this.activeRoutingTab = tab;
      if (tab === 'status') this.loadKernelRoutes();
    },

async loadRoutingTables() {
      try {
        const res = await this.api.getRoutingTables();
        this.routingTables = res.tables || [];
        // Prefer the main routing table when it is available.
        if (this.routingTables.length > 0 && !this.routingTables.find(t => t.name === this.routingTable)) {
          this.routingTable = this.routingTables[0].name;
        }
      } catch (err) {
        console.error('loadRoutingTables error:', err);
        this.routingTables = [{ id: 254, name: 'main' }, { id: null, name: 'all' }];
      }
    },

async loadKernelRoutes() {
      this.kernelRoutesError = '';
      this.kernelRoutesLoading = true;
      try {
        const res = await this.api.getKernelRoutes(this.routingTable);
        this.kernelRoutes = res.routes || [];
      } catch (err) {
        console.error('loadKernelRoutes error:', err);
        this.kernelRoutesError = err.message || 'Failed to load kernel routes';
        this.kernelRoutes = [];
      } finally {
        this.kernelRoutesLoading = false;
      }
    },

async loadStaticRoutes() {
      try {
        const res = await this.api.getStaticRoutes();
        this.staticRoutes = res.routes || [];
      } catch (err) {
        console.error('loadStaticRoutes error:', err);
      }
    },

async testRoute() {
      if (!this.routeTestIp) return;
      this.routeTestLoading = true;
      this.routeTestResult = null;
      this.routeTestMatchedRule = null;
      this.routeTestSteps = [];
      this.routeTestError = '';
      try {
        const res = await this.api.testRoute(this.routeTestIp, this.routeTestSrc || undefined);
        this.routeTestResult      = res.result;
        this.routeTestMatchedRule = res.matchedRule || null;
        this.routeTestSteps       = res.steps || [];
      } catch (err) {
        this.routeTestError = err.message || 'Error';
      } finally {
        this.routeTestLoading = false;
      }
    },

    /**
     * Display label for a gateway IP in Route Lookup results.
     * Add the gateway name in parentheses when the IP is known.
     * Mark an unknown match as the default gateway.
     */

_routeGatewayLabel(ip) {
      if (!ip) return '—';
      const gw = (this.gateways || []).find(g => g.gatewayIP === ip);
      if (gw) return `${ip} (${gw.name})`;
      return `${ip} (default gateway)`;
    },

async createRoute() {
      try {
        const data = {
          description: this.routeCreate.description,
          destination: this.routeCreate.destination,
          metric: this.routeCreate.metric !== '' ? Number(this.routeCreate.metric) : null,
          table: this.routeCreate.table || 'main',
        };
        if (this.routeCreate.viaMode === 'gateway') {
          data.gatewayId = this.routeCreate.gatewayId;
        } else if (this.routeCreate.viaMode === 'group') {
          data.gatewayGroupId = this.routeCreate.gatewayGroupId;
        } else {
          data.gateway = this.routeCreate.gateway;
          data.dev = this.routeCreate.dev;
        }
        await this.api.createStaticRoute(data);
        this.showRouteCreate = false;
        this.routeCreate = {
          description: '', destination: '',
          viaMode: 'manual', gateway: '', dev: '',
          gatewayId: '', gatewayGroupId: '',
          metric: '', table: 'main',
        };
        await this.loadStaticRoutes();
      } catch (err) {
        this.showToast(err.message || 'Failed to create route', 'error');
      }
    },

    // Helper: display label for next-hop column in static routes table

_routeNextHopLabel(route) {
      if (route.gatewayGroupId) {
        const grp = (this.gatewayGroups || []).find(g => g.id === route.gatewayGroupId);
        return grp ? grp.name : route.gatewayGroupId;
      }
      if (route.gatewayId) {
        const gw = (this.gateways || []).find(g => g.id === route.gatewayId);
        return gw ? gw.name : route.gatewayId;
      }
      return route.gateway || '—';
    },

    // Helper: badge type for next-hop column

_routeNextHopType(route) {
      if (route.gatewayGroupId) return 'group';
      if (route.gatewayId) return 'gateway';
      return 'manual';
    },

    // Helper: determine viaMode from a saved route object

_routeViaMode(route) {
      if (route.gatewayGroupId) return 'group';
      if (route.gatewayId) return 'gateway';
      return 'manual';
    },

openEditRoute(route) {
      this.routeEdit = {
        id:            route.id,
        description:   route.description || '',
        destination:   route.destination || '',
        viaMode:       this._routeViaMode(route),
        gateway:       route.gateway || '',
        dev:           route.dev || '',
        gatewayId:     route.gatewayId || '',
        gatewayGroupId: route.gatewayGroupId || '',
        metric:        route.metric != null ? String(route.metric) : '',
        table:         route.table || 'main',
      };
      this.showRouteEdit = true;
    },

async saveEditRoute() {
      try {
        const data = {
          description: this.routeEdit.description,
          destination: this.routeEdit.destination,
          metric: this.routeEdit.metric !== '' ? Number(this.routeEdit.metric) : null,
          table: this.routeEdit.table || 'main',
        };
        if (this.routeEdit.viaMode === 'gateway') {
          data.gatewayId = this.routeEdit.gatewayId;
        } else if (this.routeEdit.viaMode === 'group') {
          data.gatewayGroupId = this.routeEdit.gatewayGroupId;
        } else {
          data.gateway = this.routeEdit.gateway;
          data.dev = this.routeEdit.dev;
        }
        await this.api.updateStaticRoute({ routeId: this.routeEdit.id, data });
        this.showRouteEdit = false;
        await this.loadStaticRoutes();
        this.showToast('Route updated');
      } catch (err) {
        this.showToast(err.message || 'Failed to update route', 'error');
      }
    },

async toggleRoute(id, enabled) {
      try {
        await this.api.toggleStaticRoute({ routeId: id, enabled });
        await this.loadStaticRoutes();
      } catch (err) {
        this.showToast(err.message || 'Failed to toggle route', 'error');
      }
    },

async deleteRoute(id) {
      if (!confirm('Delete this route?')) return;
      try {
        await this.api.deleteStaticRoute({ routeId: id });
        await this.loadStaticRoutes();
      } catch (err) {
        this.showToast(err.message || 'Failed to delete route', 'error');
      }
    },

    // ========================================================================
    // NAT Methods
    // ========================================================================
};
