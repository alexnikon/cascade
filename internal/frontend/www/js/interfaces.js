/**
 * Interfaces feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const interfaceMethods = {
async loadTunnelInterfaces() {
      const seq = (this.interfaceLoadSeq || 0) + 1;
      this.interfaceLoadSeq = seq;
      this.interfacesLoading = true;
      this.interfacesError = '';
      try {
        const data = await this.api.getTunnelInterfaces();
        if (seq !== this.interfaceLoadSeq) return;
        this.tunnelInterfaces = data.interfaces || [];
        this.interfacesLoaded = true;
      } catch (err) {
        if (seq !== this.interfaceLoadSeq) return;
        this.interfacesError = 'Failed to load interfaces.';
        console.error('Failed to load tunnel interfaces:', err);
      } finally {
        if (seq === this.interfaceLoadSeq) this.interfacesLoading = false;
      }
    },

async createTunnelInterface() {
      if (this.interfaceMutationInFlight) return;
      this.interfaceMutationInFlight = true;
      try {
        if (!this.interfaceCreate.name) {
          this.showToast('Please enter interface name', 'error');
          return;
        }

        // Tunnel Address is required for peer allocation and routing hooks.
        if (!this.interfaceCreate.address || !this.interfaceCreate.address.includes('/')) {
          this.showToast('Please enter Tunnel Address in CIDR format (e.g. 10.100.0.1/24)', 'error');
          return;
        }

        if (this.interfaceCreate.protocol.startsWith('amneziawg-')) {
          if (!this.interfaceCreate.settings.h1 || !this.interfaceCreate.settings.h2 ||
              !this.interfaceCreate.settings.h3 || !this.interfaceCreate.settings.h4) {
            this.showToast('Please set H1-H4 parameters for AmneziaWG', 'error');
            return;
          }
        }
		if (this.interfaceCreate.protocol === 'amneziawg-3.1' && !this.globalSettings.awg3Supported) {
		  this.showToast(this.globalSettings.awg3SupportError || 'This runtime does not support AWG 3.1', 'error');
		  return;
		}

        const payload = {
          name: this.interfaceCreate.name,
          protocol: this.interfaceCreate.protocol,
          address: this.interfaceCreate.address,
          listenPort: this.interfaceCreate.listenPort ? parseInt(this.interfaceCreate.listenPort, 10) : undefined,
          disableRoutes: this.interfaceCreate.disableRoutes || false,
          dns: this.interfaceCreate.dns || '',
        };

        if (this.interfaceCreate.protocol.startsWith('amneziawg-')) {
          payload.settings = this.interfaceCreate.settings;
        }

        const newIface = await this.api.createTunnelInterface(payload);
        this.showInterfaceCreate = false;
        this._resetInterfaceCreate();

        await this.loadTunnelInterfaces();
        this.loadNatInterfaces();
        this.loadFirewallInterfaces();
        // Auto-switch to the new interface tab
        if (newIface && newIface.id) {
          this.activeInterfaceId = newIface.id;
        }
      } catch (err) {
        console.error('Failed to create interface:', err);
        this.showToast(`Failed: ${err.message}`, 'error');
      } finally {
        this.interfaceMutationInFlight = false;
      }
    },

    // ========================================================================
    // Quick Create Interface
    // ========================================================================

async quickCreateTunnelInterface() {
      if (this.interfaceMutationInFlight) return;
      this.interfaceMutationInFlight = true;
      try {
        const body = {
          protocol: this.interfaceCreate.protocol,
        };
        // Name is optional — server defaults to interface ID when omitted.
        const trimmedName = (this.interfaceCreate.name || '').trim();
        if (trimmedName) {
          body.name = trimmedName;
        }

        const data = await this.api.call({ method: 'post', path: '/tunnel-interfaces/quick-create', body });
        this.showInterfaceCreate = false;
        this._resetInterfaceCreate();
        await this.loadTunnelInterfaces();
        this.loadNatInterfaces();
        this.loadFirewallInterfaces();

        const iface = data.interface || {};
        const addr  = iface.address    || '';
        const port  = iface.listenPort || '';
		const proto = iface.protocol === 'amneziawg-3.1' ? ' · AWG3.1' : (iface.protocol === 'amneziawg-2.0' ? ' · AWG2' : '');

        if (data.started) {
          this.showToast(`✅ ${iface.id} created & started\n${addr} · UDP ${port}${proto}`, 'success');
          this.activeInterfaceId = iface.id;
        } else {
          this.showToast(
            `⚠️ ${iface.id} created but failed to start\n${data.startError || 'Unknown error'}`,
            'error'
          );
        }
      } catch (err) {
        console.error('Quick create failed:', err);
        this.showToast(`Failed: ${err.message}`, 'error');
      } finally {
        this.interfaceMutationInFlight = false;
      }
    },

onConfFileSelected(event) {
      const file = event.target.files && event.target.files[0];
      if (!file) return;
      // Auto-fill Name from filename (strip .conf / .txt extension).
      if (!this.importConfForm.name) {
        this.importConfForm.name = file.name.replace(/\.(conf|txt)$/i, '');
      }
      this.importConfForm.fileName = file.name;
      const reader = new FileReader();
      reader.onload = (e) => {
        this.importConfForm.conf = e.target.result || '';
      };
      reader.readAsText(file);
      // Reset input so the same file can be re-selected if needed.
      event.target.value = '';
    },

async doImportConf() {
      const name = (this.importConfForm.name || '').trim();
      const conf = (this.importConfForm.conf || '').trim();
      if (!name) { this.showToast('Please enter a name', 'error'); return; }
      if (!conf)  { this.showToast('Please paste the .conf content', 'error'); return; }

      this.importConfWarning = '';
      try {
        const isServer = this.importConfMode === 'server';
        const res = isServer
          ? await this.api.importTunnelConfServer({ name, conf })
          : await this.api.importTunnelConf({ name, conf });
        this.showImportBackup = false;
        this.importConfForm = { name: '', conf: '', fileName: '' };
        await this.loadTunnelInterfaces();
        this.loadNatInterfaces();
        this.loadFirewallInterfaces();

        const iface = res.interface || {};
		const proto = iface.protocol === 'amneziawg-3.1' ? ' · AWG3.1' : (iface.protocol === 'amneziawg-2.0' ? ' · AWG2' : ' · WG1');
        if (res.conflictWarning) {
          this.showToast(`⚠️ ${res.conflictWarning}`, 'error');
        }
        if (res.started) {
          const extra = isServer ? ` · ${res.peersCreated} peers` : '';
          this.showToast(`✅ ${iface.id} imported & started · ${iface.address}${proto}${extra}`);
          this.activeInterfaceId = iface.id;
        } else {
          this.showToast(
            `⚠️ ${iface.id} imported but failed to start\n${res.startError || 'Unknown error'}`,
            'error'
          );
        }
        if (isServer && (res.peersFailed || []).length > 0) {
          this.showToast(`⚠️ Failed to import peers: ${res.peersFailed.join(', ')}`, 'error');
        }
      } catch (err) {
        console.error('Import conf failed:', err);
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

onBackupFileSelected(event) {
      const file = event.target.files[0];
      if (!file) return;
      this.importBackupForm.fileName = file.name;
      const reader = new FileReader();
      reader.onload = (e) => {
        const text = e.target.result || '';
        this.importBackupForm.json = text;
        // Auto-detect listen port from JSON
        try {
          const parsed = JSON.parse(text);
          const port = parsed.interface && parsed.interface.listenPort;
          if (port && !this.importBackupForm.listenPort) {
            this.importBackupForm.listenPort = String(port);
          }
        } catch (_) {}
      };
      reader.readAsText(file);
    },

openExportInterface(iface) {
      this.exportInterfaceId = iface.id;
      this.exportInterfaceIncludePeers = true;
      this.showExportInterface = true;
    },

async doExportInterface() {
      if (!this.exportInterfaceId) return;
      try {
        const blob = await this.api.exportTunnelInterface({
          interfaceId: this.exportInterfaceId,
          includePeers: this.exportInterfaceIncludePeers,
        });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `${this.exportInterfaceId}-export.json`;
        a.click();
        URL.revokeObjectURL(url);
        this.showExportInterface = false;
      } catch (err) {
        this.showToast(`Export failed: ${err.message}`, 'error');
      }
    },

async doImportInterface() {
      const json = (this.importBackupForm.json || '').trim();
      const port = parseInt(this.importBackupForm.listenPort, 10);
      if (!json)        { this.showToast('Please select a backup file', 'error'); return; }
      if (!port || port < 1 || port > 65535) {
        this.showToast('Please enter a valid UDP port (1–65535)', 'error'); return;
      }
      try {
        const res = await this.api.importTunnelInterface({ json, listenPort: port });
        this.showImportBackup = false;
        this.importBackupForm = { json: '', listenPort: '', fileName: '' };
        await this.loadTunnelInterfaces();
        this.loadNatInterfaces();
        this.loadFirewallInterfaces();
        const iface = res.interface || {};
		const proto = iface.protocol === 'amneziawg-3.1' ? ' · AWG3.1' : (iface.protocol === 'amneziawg-2.0' ? ' · AWG2' : ' · WG1');
        const msg = res.started
          ? `✅ Interface restored: ${iface.id} · ${iface.address}${proto} · ${res.peersCreated} peers`
          : `⚠️ Interface restored but failed to start: ${res.startError || ''}`;
        this.showToast(msg, res.started ? 'success' : 'error');
        if (res.started) this.activeInterfaceId = iface.id;
      } catch (err) {
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

    // Generate and fill the version-appropriate manual interface fields.
    // by generating a random profile via the /templates/generate endpoint.

async generateAndFillInterfaceParams() {
      try {
        const protocolVersion = this.interfaceCreate.protocol === 'amneziawg-2.0' ? '2.0' : '3.1';
        const { params } = await this.api.generateTemplate({ profile: 'random', intensity: 'medium', protocolVersion });
        Object.assign(this.interfaceCreate.settings, params);
        this.showToast(`AWG ${protocolVersion} parameters generated`, 'success');
      } catch (err) {
        console.error('generateAndFillInterfaceParams failed:', err);
        this.showToast(`Failed to generate params: ${err.message}`, 'error');
      }
    },

    // _resetInterfaceCreate resets the create-modal state including createMode.
    // Called after both quick-create and manual create complete.

_resetInterfaceCreate() {
      this.createMode = 'quick';
      this.interfaceCreate = {
		name: '', protocol: 'amneziawg-3.1', address: '', listenPort: '',
        disableRoutes: false, dns: '', selectedTemplateId: '',
        settings: {
          jc: 6, jmin: 10, jmax: 50, s1: 64, s2: 67, s3: 64, s4: 4,
          h1: '', h2: '', h3: '', h4: '',
		  i1: '', i2: '', i3: '', i4: '', i5: '',
		  headerProtectionKey: this.generateHeaderProtectionKey(), contentPaddingAddition: '10-100',
		  rekeyAfterTime: '100-120', rekeyTimeout: '3-7', rejectAfterTime: '150-180',
		  keepaliveTimeout: '5-15', maxHandshakeAttempts: '15-20',
		  randomTrailers: true, disableCookies: true,
        },
      };
    },

    // ========================================================================
    // Version / Update check
    // ========================================================================

async checkForUpdates() {
      this.updateChecking = true;
      try {
        const res = await fetch('./api/version/check', { method: 'POST' });
        if (res.ok) {
          const prev = this.versionInfo;
          this.versionInfo = await res.json();
          this.updateBannerDismissed = false;
          if (this.versionInfo.error) {
            this.showToast(`Update check failed: ${this.versionInfo.error}`, 'error');
          } else if (!this.versionInfo.latestVersion) {
            this.showToast('No releases published yet', 'info', 4000);
          } else if (this.versionInfo.updateStatus === 'unknown') {
            this.showToast(`Latest release: ${this.versionInfo.latestVersion}. Current development build cannot be compared.`, 'info', 6000);
          } else if (this.versionInfo.updateAvailable) {
            this.showToast(`Update available: ${this.versionInfo.latestVersion}`, 'info', 6000);
          } else {
            this.showToast("You're up to date", 'success', 4000);
          }
        }
      } catch (_) {
        this.showToast('Update check failed', 'error');
      } finally {
        this.updateChecking = false;
      }
    },

async loadVersionInfo() {
      try {
        const res = await fetch('./api/version');
        if (res.ok) {
          const prev = this.versionInfo;
          this.versionInfo = await res.json();
          this.updateBannerDismissed = false;
          if (this.versionInfo.updateAvailable && !(prev && prev.updateAvailable)) {
            this.showToast(`Update available: ${this.versionInfo.latestVersion}`, 'info', 6000);
          }
        }
      } catch (_) {
        // silently ignore — non-critical
      }
    },

    // ========================================================================
    // Edit Interface
    // ========================================================================

openInterfaceEdit(iface) {
      const s = iface.settings || {};
      this.interfaceEdit = {
        id: iface.id,
        name: iface.name || iface.id,
        address: iface.address || '',
        listenPort: iface.listenPort || '',
        disableRoutes: !!iface.disableRoutes,
        natDisabled: !!iface.natDisabled,
        dns: iface.dns || '',
        publicHost: iface.publicHost || '',
        mtu: iface.mtu || 0,
        mss: iface.mss || 0,
        kernelMtu: iface.kernelMtu || 0,
        protocol: iface.protocol || 'wireguard-1.0',
        selectedTemplateId: '',
        settings: {
          jc:   s.jc   ?? 6,   jmin: s.jmin ?? 10,  jmax: s.jmax ?? 50,
          s1:   s.s1   ?? 64,  s2:   s.s2   ?? 67,  s3:   s.s3  ?? 64,  s4: s.s4 ?? 4,
          h1:   s.h1   || '',  h2:   s.h2   || '',  h3:   s.h3  || '',  h4: s.h4 || '',
          i1:   s.i1   || '',  i2:   s.i2   || '',  i3:   s.i3  || '',  i4: s.i4 || '',  i5: s.i5 || '',
		  headerProtectionKey: s.headerProtectionKey || '',
		  contentPaddingAddition: s.contentPaddingAddition || '',
		  rekeyAfterTime: s.rekeyAfterTime || '', rekeyTimeout: s.rekeyTimeout || '',
		  rejectAfterTime: s.rejectAfterTime || '', keepaliveTimeout: s.keepaliveTimeout || '',
		  maxHandshakeAttempts: s.maxHandshakeAttempts || '',
		  randomTrailers: s.randomTrailers, disableCookies: s.disableCookies,
        },
      };
      this.showInterfaceEdit = true;
    },

onMssSelectChange(e) {
      const mode = e.target.value;
      if (mode === 'disabled') this.interfaceEdit.mss = 0;
      else if (mode === 'auto') this.interfaceEdit.mss = -1;
      else if (mode === 'manual') this.interfaceEdit.mss = this.interfaceEdit.mss > 0 ? this.interfaceEdit.mss : 1380;
    },

onEditInterfaceTemplateSelect(templateId) {
      if (!templateId) return;
      const tmpl = (this.templates || []).find(t => t.id === templateId);
      if (!tmpl) return;
	  const version = this.interfaceEdit.protocol === 'amneziawg-3.1' ? '3.1' : '2.0';
	  if ((tmpl.protocolVersion || '2.0') !== version) {
		this.showToast(`This template is for AWG ${tmpl.protocolVersion || '2.0'}`, 'error');
		return;
	  }
      this.interfaceEdit.settings = {
        jc:   tmpl.jc,   jmin: tmpl.jmin,  jmax: tmpl.jmax,
        s1:   tmpl.s1,   s2:   tmpl.s2,    s3:   tmpl.s3,   s4: tmpl.s4,
        h1:   tmpl.h1,   h2:   tmpl.h2,    h3:   tmpl.h3,   h4: tmpl.h4,
        i1:   tmpl.i1 || '', i2: tmpl.i2 || '', i3: tmpl.i3 || '',
        i4:   tmpl.i4 || '', i5: tmpl.i5 || '',
		headerProtectionKey: tmpl.headerProtectionKey || '',
		contentPaddingAddition: tmpl.contentPaddingAddition || '',
		rekeyAfterTime: tmpl.rekeyAfterTime || '', rekeyTimeout: tmpl.rekeyTimeout || '',
		rejectAfterTime: tmpl.rejectAfterTime || '', keepaliveTimeout: tmpl.keepaliveTimeout || '',
		maxHandshakeAttempts: tmpl.maxHandshakeAttempts || '',
		randomTrailers: tmpl.randomTrailers, disableCookies: tmpl.disableCookies,
      };
    },

async saveInterfaceEdit() {
      const { id, name, address, listenPort, disableRoutes, natDisabled, dns, publicHost, mtu, mss, protocol, settings } = this.interfaceEdit;

      if (!name) { this.showToast('Please enter a name', 'error'); return; }
      if (!address || !address.includes('/')) {
        this.showToast('Please enter Tunnel Address in CIDR format (e.g. 10.100.0.1/24)', 'error');
        return;
      }
	  if (protocol.startsWith('amneziawg-')) {
	    if (!settings.h1 || !settings.h2 || !settings.h3 || !settings.h4) {
		  this.showToast('Please set H1-H4 parameters for AmneziaWG', 'error');
          return;
        }
      }

      if (this.interfaceMutationInFlight) return;
      this.interfaceMutationInFlight = true;

      const payload = {
        name,
        address,
        listenPort: listenPort !== '' && listenPort !== null ? Number(listenPort) : undefined,
        disableRoutes,
        natDisabled,
        dns: dns || '',
        publicHost: publicHost || '',
        mtu: mtu || 0,
        mss: mss || 0,
      };
	  if (protocol.startsWith('amneziawg-')) {
        payload.settings = { ...settings };
      }

      try {
        const res = await this.api.updateTunnelInterface({ interfaceId: id, ...payload });
        this._applyInterfaceUpdate(res);
        this.showInterfaceEdit = false;
        this.showToast(`Interface "${name}" updated successfully`);
      } catch (err) {
        console.error('saveInterfaceEdit failed:', err);
        this.showToast(`Failed: ${err.message}`, 'error');
      } finally {
        this.interfaceMutationInFlight = false;
      }
    },

async deleteTunnelInterface(iface) {
      this.interfaceDelete = iface;
    },

async confirmDeleteInterface() {
      const iface = this.interfaceDelete;
      if (!iface) return;
      this.interfaceDelete = null;
      try {
        await this.api.deleteTunnelInterface({ interfaceId: iface.id });
        if (this.activeInterfaceId === iface.id) {
          this.activeInterfaceId = null;
          this.selectedInterface = null;
          this.selectedInterfacePeers = [];
        }
        await this.loadTunnelInterfaces();
        this.loadNatInterfaces();
        this.loadFirewallInterfaces();
        this.showToast(`Interface "${iface.name}" deleted`);
      } catch (err) {
        console.error('Delete failed:', err);
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

    // Update one interface reactively with Vue 2 splice.
    // Called after start/stop/restart; use the API response to avoid an extra
    // GET and avoid depending on the current network state.

_applyInterfaceUpdate(updatedIface) {
      const idx = this.tunnelInterfaces.findIndex(i => i.id === updatedIface.id);
      if (idx !== -1) {
        this.tunnelInterfaces.splice(idx, 1, updatedIface);
      } else {
        // The interface appeared for the first time, for example after create + immediate start.
        this.tunnelInterfaces.push(updatedIface);
      }
    },

async startTunnelInterface(iface) {
      if (this.loadingInterfaceId) return; // Prevent duplicate clicks.
      this.loadingInterfaceId = iface.id;
      try {
        const data = await this.api.startTunnelInterface({ interfaceId: iface.id });
        if (data && data.interface) this._applyInterfaceUpdate(data.interface);
        this.loadNatInterfaces();
        this.loadFirewallInterfaces();
      } catch (err) {
        console.error('Start failed:', err);
        this.showToast(`Start failed: ${err.message}`, 'error');
      } finally {
        this.loadingInterfaceId = null;
      }
    },

async stopTunnelInterface(iface) {
      if (this.loadingInterfaceId) return;
      this.loadingInterfaceId = iface.id;
      try {
        const data = await this.api.stopTunnelInterface({ interfaceId: iface.id });
        if (data && data.interface) this._applyInterfaceUpdate(data.interface);
        this.loadNatInterfaces();
        this.loadFirewallInterfaces();
      } catch (err) {
        console.error('Stop failed:', err);
        this.showToast(`Stop failed: ${err.message}`, 'error');
      } finally {
        this.loadingInterfaceId = null;
      }
    },

async restartTunnelInterface(iface) {
      if (this.loadingInterfaceId) return;
      this.loadingInterfaceId = iface.id;
      try {
        const data = await this.api.restartTunnelInterface({ interfaceId: iface.id });
        if (data && data.interface) this._applyInterfaceUpdate(data.interface);
        this.loadNatInterfaces();
        this.loadFirewallInterfaces();
      } catch (err) {
        console.error('Restart failed:', err);
        this.showToast(`Restart failed: ${err.message}`, 'error');
      } finally {
        this.loadingInterfaceId = null;
      }
    },

async loadInterfacePeers(interfaceId) {
      if (interfaceId !== this.activeInterfaceId) return;
      return this.refreshPeers();
    },

async createPeer() {
      if (!this.activeInterfaceId) {
        this.showToast('No interface selected', 'error');
        return;
      }

      const { mode, peerType, name, publicKey, endpoint, allowedIPs, clientAllowedIPs, persistentKeepalive, groupId, expiredAt } = this.peerCreate;

      // Validation
      if (!name || name.trim() === '') {
        this.showToast('Please enter a name', 'error');
        return;
      }
      if (mode === 'manual' && !publicKey) {
        this.showToast('Please enter the public key', 'error');
        return;
      }
      // Interconnect requires explicit AllowedIPs (it routes a subnet, not just /32)
      if (peerType === 'interconnect' && !allowedIPs) {
        this.showToast('Please enter Allowed IPs for the interconnect peer (e.g., 192.168.2.0/24)', 'error');
        return;
      }

      // For client+generate with no allowedIPs → auto-allocate /32 from interface subnet
      const autoAllocate = mode === 'generate' && peerType === 'client' && !allowedIPs;

      if (this.peerMutationInFlight) return;
      this.peerMutationInFlight = true;

      const payload = {
        name,
        peerType,
        ...(mode === 'generate' ? { generateKeys: true } : { publicKey }),
        ...(autoAllocate ? { autoAllocateIP: true } : { allowedIPs }),
        clientAllowedIPs: clientAllowedIPs || undefined,
        endpoint: endpoint || undefined,
        persistentKeepalive: persistentKeepalive || 25,
        ...(peerType === 'client' && groupId ? { groupId } : {}),
        ...(expiredAt ? { expiredAt } : {}),
      };

      try {
        const res = await this.api.createTunnelInterfacePeer({
          interfaceId: this.activeInterfaceId,
          ...payload,
        });

        const interfaceId = this.activeInterfaceId;
        const peerId = res.peer && res.peer.id;

        const showQR = this.peerCreate.showQR;
        this.showPeerCreate = false;
        this.inlineGroupShow = false;
        this.inlineGroupInput = '';
        this.peerCreate = { mode: 'generate', peerType: 'client', name: '', publicKey: '', endpoint: '', allowedIPs: '', clientAllowedIPs: '', persistentKeepalive: 25, groupId: this.defaultGroupId(), expiredAt: '', showQR: false };

        await this.refreshPeers();
        await this.loadTunnelInterfaces();
        if (peerType === 'client') {
          this.loadClientGroups();
          this.loadAliases();
        }

        if (showQR && mode === 'generate' && peerType === 'client' && peerId) {
          this.qrcode = this.peerQrUrl(interfaceId, peerId);
        } else {
          this.showToast(peerType === 'client' ? 'Client created!' : 'Peer created!');
        }
      } catch (err) {
        console.error('Failed to create peer:', err);
        this.showToast(`Failed: ${err.message}`, 'error');
      } finally {
        this.peerMutationInFlight = false;
      }
    },

async deletePeer(peer) {
      const label = peer.peerType === 'interconnect' ? 'peer' : 'client';
      if (!confirm(`Delete ${label} "${peer.name}"?`)) return;
      try {
        await this.api.deleteTunnelInterfacePeer({
          interfaceId: this.selectedInterface.id,
          peerId: peer.id,
        });
        await this.loadInterfacePeers(this.selectedInterface.id);
        await this.loadTunnelInterfaces();
        this.showToast(peer.peerType === 'interconnect' ? 'Peer deleted!' : 'Client deleted!');
      } catch (err) {
        console.error('Delete failed:', err);
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

async downloadPeerConfig(peer) {
      try {
        const config = await this.api.getPeerConfig({ interfaceId: this._peerIfaceId(peer), peerId: peer.id });
        const blob = new Blob([config], { type: 'text/plain' });
        const url = window.URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `${peer.name.replace(/\s+/g, '-')}.conf`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        window.URL.revokeObjectURL(url);
      } catch (err) {
        console.error('Download failed:', err);
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

    // ========================================================================
    // Gateways Methods
    // ========================================================================
};
