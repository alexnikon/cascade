/**
 * Gateways feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const gatewayMethods = {
async loadGateways({ silent = false } = {}) {
      const remoteKey = this.activeRemoteId || 'local';
      if (this.gatewaysRefreshPromise && this.gatewaysRefreshPromiseKey === remoteKey) {
        return this.gatewaysRefreshPromise;
      }
      this.gatewaysLoading = true;
      this.gatewaysError = '';
      const promise = (async () => {
      try {
        const res = await this.api.getGateways();
        if (remoteKey !== (this.activeRemoteId || 'local')) return;
        this.gateways = res.gateways || [];
        this.gatewaysLoaded = true;
      } catch (err) {
        if (remoteKey !== (this.activeRemoteId || 'local')) return;
        this.gatewaysError = 'Failed to load gateways.';
        if (!silent) console.error('loadGateways error:', err);
      }
      })();
      this.gatewaysRefreshPromise = promise;
      this.gatewaysRefreshPromiseKey = remoteKey;
      try {
        return await promise;
      } finally {
        if (this.gatewaysRefreshPromise === promise) {
          this.gatewaysRefreshPromise = null;
          this.gatewaysRefreshPromiseKey = null;
          this.gatewaysLoading = false;
        }
      }
    },

async refreshGateways() {
      if (!this.authenticated) return;
      return this.loadGateways({ silent: true });
    },

async loadGatewayGroups() {
      try {
        const res = await this.api.getGatewayGroups();
        this.gatewayGroups = res.groups || [];
      } catch (err) {
        console.error('loadGatewayGroups error:', err);
      }
    },

async loadSystemInterfaces() {
      try {
        const res = await this.api.getSystemInterfaces();
        this.systemInterfaces = res.interfaces || [];
      } catch (err) {
        console.error('loadSystemInterfaces error:', err);
      }
    },

    // ── Create Gateway ────────────────────────────────────────────────────────

async createGateway() {
      const f = this.gatewayCreate;
      if (!f.name.trim())      return this.showToast('Gateway name is required', 'error');
      if (!f.interface)        return this.showToast('Interface is required', 'error');
      if (!f.gatewayIP.trim()) return this.showToast('Gateway IP is required', 'error');
      if (this.gatewayMutationInFlight) return;
      this.gatewayMutationInFlight = true;
      try {
        await this.api.createGateway({
          name:             f.name.trim(),
          interface:        f.interface,
          gatewayIP:        f.gatewayIP.trim(),
          monitorAddress:   f.monitorAddress.trim(),
          monitor:          f.monitor,
          monitorInterval:  Number(f.monitorInterval),
          windowSeconds:    f.windowSeconds !== null ? Number(f.windowSeconds) : null,
          latencyThreshold: Number(f.latencyThreshold),
          monitorHttp: {
            enabled:        f.monitorHttp.enabled,
            url:            f.monitorHttp.url.trim(),
            expectedStatus: Number(f.monitorHttp.expectedStatus),
            interval:       Number(f.monitorHttp.interval),
            timeout:        Number(f.monitorHttp.timeout),
          },
          monitorRule:      f.monitorRule,
          description:      f.description.trim(),
        });
        this.showGatewayCreate = false;
        this.gatewayCreate = {
          name: '', interface: '', gatewayIP: '', monitorAddress: '',
          monitor: true, monitorInterval: 5, windowSeconds: null,
          latencyThreshold: 500,
          monitorHttp: { enabled: false, url: '', expectedStatus: 200, interval: 10, timeout: 5 },
          monitorRule: 'icmp_only',
          description: '',
        };
        await this.loadGateways();
      } catch (err) {
        this.showToast(`Failed: ${err.message}`, 'error');
      } finally {
        this.gatewayMutationInFlight = false;
      }
    },

    // ── Edit Gateway ──────────────────────────────────────────────────────────

openGatewayEdit(gw) {
      const httpDefaults = { enabled: false, url: '', expectedStatus: 200, interval: 10, timeout: 5 };
      this.gatewayEdit = {
        id:               gw.id,
        name:             gw.name,
        interface:        gw.interface,
        gatewayIP:        gw.gatewayIP || '',
        monitorAddress:   gw.monitorAddress || '',
        monitor:          gw.monitor,
        monitorInterval:  gw.monitorInterval,
        windowSeconds:    gw.windowSeconds ?? null,
        latencyThreshold: gw.latencyThreshold || 500,
        monitorHttp:      gw.monitorHttp ? { ...httpDefaults, ...gw.monitorHttp } : { ...httpDefaults },
        monitorRule:      gw.monitorRule || 'icmp_only',
        description:      gw.description || '',
        adminDown:        !!gw.adminDown,
      };
      this.showGatewayEdit = true;
    },

async saveGatewayEdit() {
      const f = this.gatewayEdit;
      if (!f.name.trim())      return this.showToast('Gateway name is required', 'error');
      if (!f.interface)        return this.showToast('Interface is required', 'error');
      if (!f.gatewayIP.trim()) return this.showToast('Gateway IP is required', 'error');
      if (this.gatewayMutationInFlight) return;
      this.gatewayMutationInFlight = true;
      try {
        await this.api.updateGateway({
          gatewayId:        f.id,
          name:             f.name.trim(),
          interface:        f.interface,
          gatewayIP:        f.gatewayIP.trim(),
          monitorAddress:   f.monitorAddress.trim(),
          monitor:          f.monitor,
          monitorInterval:  Number(f.monitorInterval),
          windowSeconds:    f.windowSeconds !== null ? Number(f.windowSeconds) : null,
          latencyThreshold: Number(f.latencyThreshold),
          monitorHttp: {
            enabled:        f.monitorHttp.enabled,
            url:            f.monitorHttp.url.trim(),
            expectedStatus: Number(f.monitorHttp.expectedStatus),
            interval:       Number(f.monitorHttp.interval),
            timeout:        Number(f.monitorHttp.timeout),
          },
          monitorRule:      f.monitorRule,
          description:      f.description.trim(),
          adminDown:        f.adminDown,
        });
        this.showGatewayEdit = false;
        const res = await this.api.getGateways();
        this.gateways = res.gateways || [];
      } catch (err) {
        this.showToast(`Failed: ${err.message}`, 'error');
      } finally {
        this.gatewayMutationInFlight = false;
      }
    },

    // ── Toggle Admin Down ─────────────────────────────────────────────────────

async toggleGatewayAdminDown(gw) {
      const newVal = !gw.adminDown;
      // Optimistic update: flip adminDown and status locally for instant feedback
      const idx = this.gateways.findIndex(g => g.id === gw.id);
      if (idx !== -1) {
        const updated = { ...this.gateways[idx], adminDown: newVal };
        if (newVal) {
          updated.realStatus = updated.status; // preserve real status for ring
          updated.status = 'admin_down';
        } else {
          updated.status = updated.realStatus || 'unknown';
          updated.realStatus = '';
        }
        this.gateways.splice(idx, 1, updated);
      }
      try {
        await this.api.updateGateway({
          gatewayId:        gw.id,
          name:             gw.name,
          interface:        gw.interface,
          gatewayIP:        gw.gatewayIP,
          monitorAddress:   gw.monitorAddress || '',
          monitor:          gw.monitor,
          monitorInterval:  gw.monitorInterval,
          windowSeconds:    gw.windowSeconds ?? null,
          latencyThreshold: gw.latencyThreshold || 500,
          monitorHttp:      gw.monitorHttp || {},
          monitorRule:      gw.monitorRule || 'icmp_only',
          description:      gw.description || '',
          adminDown:        newVal,
        });
        // Next polling cycle will bring fresh realStatus from server
      } catch (err) {
        // Revert on error
        const res = await this.api.getGateways();
        this.gateways = res.gateways || [];
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

    // ── Delete Gateway ────────────────────────────────────────────────────────

async deleteGateway(gw) {
      if (!confirm(`Delete gateway "${gw.name}"?`)) return;
      try {
        await this.api.deleteGateway({ gatewayId: gw.id });
        const res = await this.api.getGateways();
        this.gateways = res.gateways || [];
      } catch (err) {
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

    // ── Create Gateway Group ──────────────────────────────────────────────────

async createGatewayGroup() {
      const f = this.groupCreate;
      if (!f.name.trim()) return this.showToast('Group name is required', 'error');
      try {
        await this.api.createGatewayGroup({
          name:        f.name.trim(),
          trigger:     f.trigger,
          description: f.description.trim(),
          gateways:    f.gateways,
        });
        this.showGroupCreate = false;
        this.groupCreate = { name: '', trigger: 'packetloss', description: '', gateways: [] };
        await this.loadGatewayGroups();
      } catch (err) {
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

    // ── Edit Gateway Group ────────────────────────────────────────────────────

openGroupEdit(grp) {
      this.groupEdit = {
        id:          grp.id,
        name:        grp.name,
        trigger:     grp.trigger,
        description: grp.description || '',
        gateways:    JSON.parse(JSON.stringify(grp.gateways || [])),
      };
      this.showGroupEdit = true;
    },

async saveGroupEdit() {
      const f = this.groupEdit;
      if (!f.name.trim()) return this.showToast('Group name is required', 'error');
      try {
        await this.api.updateGatewayGroup({
          groupId:     f.id,
          name:        f.name.trim(),
          trigger:     f.trigger,
          description: f.description.trim(),
          gateways:    f.gateways,
        });
        this.showGroupEdit = false;
        const res = await this.api.getGatewayGroups();
        this.gatewayGroups = res.groups || [];
      } catch (err) {
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

    // ── Delete Gateway Group ──────────────────────────────────────────────────

async deleteGatewayGroup(grp) {
      if (!confirm(`Delete gateway group "${grp.name}"?`)) return;
      try {
        await this.api.deleteGatewayGroup({ groupId: grp.id });
        const res = await this.api.getGatewayGroups();
        this.gatewayGroups = res.groups || [];
      } catch (err) {
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

    // ── Remote servers ────────────────────────────────────────────────────────

async loadRemotes() {
      if (!this.authenticated) return;
      try {
        const res = await this.api.getRemotes();
        this.remotes = res.remotes || [];
      } catch (err) {
        this.showToast(`Failed to load remotes: ${err.message}`, 'error');
      }
    },

async switchToRemote(remote) {
      this.api.setRemote(remote.id);
      this.activeRemoteId = remote.id;
      this.peerRefreshSeq += 1;
      this.allPeerRefreshSeq += 1;
      // Reset current page data so stale local data isn't shown.
      this.tunnelInterfaces = [];
      this.interfacesLoaded = false;
      this.interfacesError = '';
      this.selectedInterfacePeers = [];
      this.selectedPeersLoaded = false;
      this.selectedPeersError = '';
      this.allPeers = [];
      this.allPeersLoaded = false;
      this.allPeersError = '';
      this.gateways = [];
      this.gatewaysLoaded = false;
      this.gatewaysError = '';
      this.natRules = [];
      this.dnatRules = [];
      this.natInterfaces = [];
      this.staticRoutes = [];
      this.kernelRoutes = [];
      this.firewallRules = [];
      this.aliases = [];
      this.switchPage('dashboard');
      try {
        await Promise.all([this.loadTunnelInterfaces(), this.loadSettings()]);
      } catch (err) {
        this.showToast(`Failed to connect to ${remote.name}: ${err.message}`, 'error');
        this.switchToLocal();
      }
    },

switchToLocal() {
      this.api.clearRemote();
      this.activeRemoteId = null;
      this.peerRefreshSeq += 1;
      this.allPeerRefreshSeq += 1;
      this.tunnelInterfaces = [];
      this.interfacesLoaded = false;
      this.interfacesError = '';
      this.selectedInterfacePeers = [];
      this.selectedPeersLoaded = false;
      this.selectedPeersError = '';
      this.allPeers = [];
      this.allPeersLoaded = false;
      this.allPeersError = '';
      this.gateways = [];
      this.gatewaysLoaded = false;
      this.gatewaysError = '';
      this.natRules = [];
      this.dnatRules = [];
      this.natInterfaces = [];
      this.staticRoutes = [];
      this.kernelRoutes = [];
      this.firewallRules = [];
      this.aliases = [];
      this.switchPage('dashboard');
      // Reload interfaces explicitly so navigation can select the new item.
      this.loadTunnelInterfaces();
      this.loadSettings();
    },

async addRemote() {
      this.remoteAddError = '';
      this.remoteAddLoading = true;
      try {
        const res = await this.api.addRemote(this.remoteAddForm);
        // Server returned totp_required: true — show TOTP step.
        if (res && res.totp_required) {
          this.remoteAddNeedsTOTP = true;
          this.remoteAddForm.totpCode = '';
          return;
        }
        this.remotes.push(res.remote);
        this.showRemoteAdd = false;
        this.remoteAddForm = { name: '', url: '', mode: 'login', username: '', password: '', totpCode: '', token: '' };
        this.remoteAddNeedsTOTP = false;
        this.showToast('Remote server added', 'success');
      } catch (err) {
        this.remoteAddError = err.message;
      } finally {
        this.remoteAddLoading = false;
      }
    },

async deleteRemote(remote) {
      if (!confirm(`Remove remote server "${remote.name}"?`)) return;
      try {
        await this.api.deleteRemote({ id: remote.id });
        this.remotes = this.remotes.filter(r => r.id !== remote.id);
        this.showToast('Remote server removed', 'success');
      } catch (err) {
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

async testRemote(remote) {
      this.$set(this.remoteTesting, remote.id, true);
      this.$set(this.remoteTestResult, remote.id, null);
      try {
        await this.api.testRemote({ id: remote.id });
        this.$set(this.remoteTestResult, remote.id, 'ok');
      } catch (err) {
        this.$set(this.remoteTestResult, remote.id, 'error');
      } finally {
        this.$set(this.remoteTesting, remote.id, false);
      }
    },

    // ── Speed test ────────────────────────────────────────────────────────────

openSpeedtest() {
      this.speedtestResult = null;
      this.speedtestError = '';
      this.speedtest.fromId = this.activeRemoteId || '__local__';
      this.speedtest.toId = this.remotes.length > 0 ? this.remotes[0].id : '__local__';
      this.speedtest.via = 'auto';
      this.speedtest.tunnelIp = '';
      this.speedtestDetectedTunnelIp = '';
      this.speedtestFromIfaces = [];
      this.speedtestToIfaces = [];
      this.speedtestFromIfaceId = '';
      this.speedtestToIfaceId = '';
      this.showSpeedtest = true;
      this.loadSpeedtestHistory();
      this.onSpeedtestServersChange();
    },

async onSpeedtestServersChange() {
      this.speedtestDetectedTunnelIp = '';
      this.speedtestFromIfaces = [];
      this.speedtestToIfaces = [];
      this.speedtestFromIfaceId = '';
      this.speedtestToIfaceId = '';
      this.speedtestError = '';
      if (this.speedtest.fromId === this.speedtest.toId) return;
      try {
        const [ip, fromIfaces, toIfaces] = await Promise.all([
          this._findTunnelIP(this.speedtest.fromId, this.speedtest.toId),
          this._getIfacesFor(this.speedtest.fromId),
          this._getIfacesFor(this.speedtest.toId),
        ]);
        this.speedtestDetectedTunnelIp = ip || '';
        this.speedtestFromIfaces = fromIfaces;
        this.speedtestToIfaces = toIfaces;
        if (fromIfaces.length) this.speedtestFromIfaceId = fromIfaces[0].id;
        if (toIfaces.length) this.speedtestToIfaceId = toIfaces[0].id;
      } catch (e) {
        this.speedtestError = 'Failed to load interfaces: ' + (e.message || e);
      }
    },

_speedtestServerName(id) {
      if (id === '__local__') return this.localServerName || this.pageTitle || 'Local';
      const r = this.remotes.find(r => r.id === id);
      return r ? r.name : id;
    },

_speedtestPublicHost(fromId) {
      if (fromId === '__local__') {
        return this.globalSettings.resolvedPublicIP || this.globalSettings.publicIP || '';
      }
      const remote = this.remotes.find(r => r.id === fromId);
      if (!remote) return '';
      try { return new URL(remote.url).hostname; } catch (_) { return ''; }
    },

    // _ipInCIDR returns true if ip (string) falls within cidr (string), excluding /0.

_ipInCIDR(ip, cidr) {
      const parts = ip.split('.').map(Number);
      if (parts.length !== 4 || parts.some(p => isNaN(p))) return false;
      const ip32 = (parts[0] << 24 | parts[1] << 16 | parts[2] << 8 | parts[3]) >>> 0;
      const n = this._cidrNetwork(cidr);
      if (!n || n.prefix === 0) return false; // exclude default route
      return ((ip32 & n.mask) >>> 0) === n.net;
    },

    // _cidrNetwork returns the network address and prefix length for a CIDR string.

_cidrNetwork(cidr) {
      const [addr, bits] = cidr.split('/');
      if (!addr || bits === undefined) return null;
      const prefix = parseInt(bits, 10);
      if (isNaN(prefix)) return null;
      const parts = addr.split('.').map(Number);
      if (parts.length !== 4 || parts.some(p => isNaN(p))) return null;
      const ip32 = (parts[0] << 24 | parts[1] << 16 | parts[2] << 8 | parts[3]) >>> 0;
      const mask = prefix === 0 ? 0 : (0xffffffff << (32 - prefix)) >>> 0;
      return { net: (ip32 & mask) >>> 0, mask, ip32, prefix };
    },

    // _sameSubnet returns true if ipA/prefixA and ipB/prefixB share a subnet.

_sameSubnet(cidrA, cidrB) {
      const a = this._cidrNetwork(cidrA);
      const b = this._cidrNetwork(cidrB);
      if (!a || !b) return false;
      const mask = Math.min(a.prefix, b.prefix) === 0 ? 0
        : (0xffffffff << (32 - Math.min(a.prefix, b.prefix))) >>> 0;
      return ((a.ip32 & mask) >>> 0) === ((b.ip32 & mask) >>> 0);
    },

    // _getIfacesFor loads active tunnel interfaces from a server (local or remote).

async _getIfacesFor(serverId) {
      const data = serverId === '__local__'
        ? await this.api.getTunnelInterfaces()
        : await this.api.remoteCall({ remoteId: serverId, method: 'get', path: '/tunnel-interfaces' });
      return (data.interfaces || []).filter(i => i.enabled && i.address);
    },

    // _getPeersFor loads peers for one interface on a server.

async _getPeersFor(serverId, ifaceId) {
      const path = `/tunnel-interfaces/${ifaceId}/peers`;
      const data = serverId === '__local__'
        ? await this.api.call({ method: 'get', path })
        : await this.api.remoteCall({ remoteId: serverId, method: 'get', path });
      return data.peers || [];
    },

    // _findTunnelIP finds a common subnet between interfaces on two servers.
    // If server A has 10.0.1.1/30 and server B has 10.0.1.2/30 — same subnet → S2S.
    // Returns the "from" interface IP, or null if no match found.

async _findTunnelIP(fromId, toId) {
      try {
        const [fromIfaces, toIfaces] = await Promise.all([
          this._getIfacesFor(fromId),
          this._getIfacesFor(toId),
        ]);
        const fromS2S = fromIfaces.filter(i => i.disableRoutes);
        const toS2S = toIfaces.filter(i => i.disableRoutes);
        for (const fi of fromS2S) {
          for (const ti of toS2S) {
            if (this._sameSubnet(fi.address, ti.address)) {
              return fi.address.split('/')[0];
            }
          }
        }
      } catch (_) {}
      return null;
    },

async runSpeedtest() {
      if (this.speedtestRunning) return;
      const { fromId, toId, duration, streams } = this.speedtest;
      if (fromId === toId) {
        this.speedtestError = 'Source and destination must be different servers.';
        return;
      }
      const host = this._speedtestPublicHost(fromId);
      if (!host) {
        this.speedtestError = 'Cannot determine IP address of source server. Set Public IP in Settings.';
        return;
      }

      this.speedtestRunning = true;
      this.speedtestResult = null;
      this.speedtestError = '';
      this.speedtestPingConfirm = false;

      try {
        let resolvedHost = host;
        let via = 'internet';

        if (this.speedtest.via === 'manual') {
          const iface = this.speedtestFromIfaces.find(i => i.id === this.speedtestFromIfaceId);
          if (!iface) {
            this.speedtestError = 'Select a source interface.';
            this.speedtestRunning = false;
            return;
          }
          resolvedHost = iface.address.split('/')[0];
          via = 'manual';
        } else if (this.speedtest.via === 'tunnel') {
          const ip = this.speedtest.tunnelIp.trim() || this.speedtestDetectedTunnelIp;
          if (!ip) {
            this.speedtestError = 'No S2S tunnel found. Switch to Manual or Internet mode.';
            this.speedtestRunning = false;
            return;
          }
          resolvedHost = ip;
          via = 'tunnel';
        } else if (this.speedtest.via === 'internet') {
          resolvedHost = host;
          via = 'internet';
        } else {
          // auto
          const tunnelIP = await this._findTunnelIP(fromId, toId);
          resolvedHost = tunnelIP || host;
          via = tunnelIP ? 'tunnel' : 'internet';
        }

        // Ping check: from server pings the resolved host.
        // For internet mode warn softly (ICMP may be blocked), others warn harder.
        if (via !== 'internet') {
          const fromRemoteId = fromId === '__local__' ? null : fromId;
          try {
            const pingRes = await this.api.ping({ host: resolvedHost, count: 3, remoteId: fromRemoteId });
            if (!pingRes.reachable) {
              this.speedtestPendingHost = resolvedHost;
              this.speedtestPendingVia = via;
              this.speedtestPingConfirmMsg = `${resolvedHost} is not reachable from the source server (ICMP). The speed test may fail.`;
              this.speedtestPingConfirm = true;
              this.speedtestRunning = false;
              return;
            }
          } catch (_) { /* ping endpoint unavailable — proceed */ }
        } else {
          // Internet mode: soft check
          const fromRemoteId = fromId === '__local__' ? null : fromId;
          try {
            const pingRes = await this.api.ping({ host: resolvedHost, count: 2, remoteId: fromRemoteId });
            if (!pingRes.reachable) {
              this.speedtestPendingHost = resolvedHost;
              this.speedtestPendingVia = via;
              this.speedtestPingConfirmMsg = `${resolvedHost} does not respond to ICMP — it may be blocked by firewall. The test may still work.`;
              this.speedtestPingConfirm = true;
              this.speedtestRunning = false;
              return;
            }
          } catch (_) {}
        }

        const toIface = this.speedtest.via === 'manual'
          ? this.speedtestToIfaces.find(i => i.id === this.speedtestToIfaceId)
          : null;
        const { jobId } = await this.api.speedtestRun({
          fromServer: this._speedtestServerName(fromId),
          toServer: this._speedtestServerName(toId),
          fromRemoteId: fromId === '__local__' ? '' : fromId,
          toRemoteId: toId === '__local__' ? '' : toId,
          host: resolvedHost,
          bindAddr: toIface ? toIface.address.split('/')[0] : '',
          via,
          duration: Number(duration),
          streams: Number(streams),
        });

        // Poll until done or error.
        for (;;) {
          await new Promise(r => setTimeout(r, 2000));
          const rec = await this.api.speedtestGetResult(jobId);
          if (rec.status === 'running') continue;
          if (rec.status === 'error') {
            this.speedtestError = rec.error || 'Speed test failed.';
          } else {
            this.speedtestResult = rec;
          }
          break;
        }
      } catch (err) {
        this.speedtestError = err.message || 'Speed test failed.';
      } finally {
        this.speedtestRunning = false;
        this.loadSpeedtestHistory();
      }
    },

async confirmAndRunSpeedtest() {
      this.speedtestPingConfirm = false;
      // Re-enter runSpeedtest but skip ping check by temporarily overriding flag.
      const { fromId, toId, duration, streams } = this.speedtest;
      const host = this._speedtestPublicHost(fromId);
      this.speedtestRunning = true;
      this.speedtestResult = null;
      this.speedtestError = '';
      try {
        const resolvedHost = this.speedtestPendingHost;
        const via = this.speedtestPendingVia;
        const toIface2 = via === 'manual'
          ? this.speedtestToIfaces.find(i => i.id === this.speedtestToIfaceId)
          : null;
        const { jobId } = await this.api.speedtestRun({
          fromServer: this._speedtestServerName(fromId),
          toServer: this._speedtestServerName(toId),
          fromRemoteId: fromId === '__local__' ? '' : fromId,
          toRemoteId: toId === '__local__' ? '' : toId,
          host: resolvedHost,
          bindAddr: toIface2 ? toIface2.address.split('/')[0] : '',
          via,
          duration: Number(duration),
          streams: Number(streams),
        });
        for (;;) {
          await new Promise(r => setTimeout(r, 2000));
          const rec = await this.api.speedtestGetResult(jobId);
          if (rec.status === 'running') continue;
          if (rec.status === 'error') this.speedtestError = rec.error || 'Speed test failed.';
          else this.speedtestResult = rec;
          break;
        }
      } catch (err) {
        this.speedtestError = err.message || 'Speed test failed.';
      } finally {
        this.speedtestRunning = false;
        this.loadSpeedtestHistory();
      }
    },

async loadSpeedtestHistory() {
      try {
        const { results } = await this.api.speedtestListResults();
        this.speedtestHistory = results || [];
      } catch (_) {}
    },

async clearSpeedtestHistory() {
      try {
        await this.api.speedtestClearResults();
        this.speedtestHistory = [];
      } catch (err) {
        this.showToast(err.message, 'error');
      }
    },

    // ── Group gateways entry helpers ──────────────────────────────────────────

addGroupGatewayEntry(form) {
      form.gateways.push({ gatewayId: '', tier: 1, weight: 100 });
    },

removeGroupGatewayEntry(form, idx) {
      form.gateways.splice(idx, 1);
    },

    // ── Status helpers ────────────────────────────────────────────────────────

gatewayStatusColor(status) {
      const map = { healthy: '#22c55e', degraded: '#eab308', down: '#ef4444', unknown: '#9ca3af', admin_down: '#6b7280' };
      return map[status] || map.unknown;
    },

gatewayStatusLabel(status) {
      const map = { healthy: 'Healthy', degraded: 'Degraded', down: 'Down', unknown: 'Unknown', admin_down: 'Admin Down' };
      return map[status] || 'Unknown';
    },

    // Look up gateway name by id (used in group display)

gatewayNameById(id) {
      const gw = this.gateways.find(g => g.id === id);
      return gw ? gw.name : id;
    },

gatewayStatusById(id) {
      const gw = this.gateways.find(g => g.id === id);
      return gw ? gw.status : 'unknown';
    },

    // ========================================================================
    // Routing Methods
    // ========================================================================
};
