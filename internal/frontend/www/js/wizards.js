/**
 * Wizards feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const wizardMethods = {
wizardVPNPickPort() {
      const usedPorts = new Set((this.tunnelInterfaces || []).map(i => i.listenPort));
      usedPorts.add(443); // always skip — Caddy QUIC
      for (let attempts = 0; attempts < 200; attempts++) {
        const p = Math.floor(Math.random() * 600) + 400; // 400–999
        if (!usedPorts.has(p)) return p;
      }
      return 8443; // last-resort fallback
    },

wizardVPNInit() {
      this.wizardVPN = {
        step: 1,
		protocol: 'amneziawg',
        ifaceName: '',
        dns: this.globalSettings.dns || '',
        peerName: 'My Device',
        running: false,
        error: '',
        ifaceId: '',
        peerId: '',
        qrUrl: '',
        ifaceAddr: '',
        ifacePort: 0,
        startWarning: '',
      };
    },

async wizardVPNRun() {
      this.wizardVPN.running = true;
      this.wizardVPN.error = '';
      try {
        const proto = this.wizardVPN.protocol;
        let awgParams = null;

        // Generate AWG obfuscation params if needed
        if (proto === 'amneziawg') {
          const host = (this.globalSettings.resolvedPublicIP || '').trim();
          const isIPv4 = /^\d+\.\d+\.\d+\.\d+$/.test(host);
          const isIPv6 = host.includes(':');
          const isDomain = host && !isIPv4 && !isIPv6;
          const profile = isDomain ? 'quic_burst' : 'tls_client_hello';
          const res = await this.api.call({
            method: 'post',
            path: '/templates/generate',
			body: { profile, intensity: 'medium', host: isDomain ? host : '', protocolVersion: '3.1' },
          });
          awgParams = res.params;
        }

        // Pick a low port (443 preferred, fallback random 400–999)
        const wizardPort = this.wizardVPNPickPort();

        // Create + start interface via quick-create
        const qcBody = {
		  protocol: proto === 'amneziawg' ? 'amneziawg-3.1' : 'wireguard-1.0',
        };
        const trimmedName = (this.wizardVPN.ifaceName || '').trim();
        if (trimmedName) qcBody.name = trimmedName;

        const qcData = await this.api.call({ method: 'post', path: '/tunnel-interfaces/quick-create', body: qcBody });
        const ifaceId = qcData.interface.id;
        this.wizardVPN.ifaceId = ifaceId;
        this.wizardVPN.ifaceAddr = qcData.interface.address || '';

        // Apply port, DNS override and/or AWG params via PATCH (always needed for port)
        const patch = { listenPort: wizardPort };
        if (this.wizardVPN.dns) patch.dns = this.wizardVPN.dns;
        if (awgParams) patch.settings = awgParams;
        await this.api.call({ method: 'patch', path: `/tunnel-interfaces/${ifaceId}`, body: patch });
        // PATCH with listenPort triggers restart automatically (wg-quick rebind)
        this.wizardVPN.ifacePort = wizardPort;

        // Create first peer
        const peerRes = await this.api.call({
          method: 'post',
          path: `/tunnel-interfaces/${ifaceId}/peers`,
          body: {
            name: (this.wizardVPN.peerName || '').trim() || 'My Device',
            peerType: 'client',
            generateKeys: true,
            autoAllocateIP: true,
            persistentKeepalive: 25,
          },
        });
        const peerId = peerRes.peer.id;
        this.wizardVPN.peerId = peerId;
        this.wizardVPN.qrUrl = this.peerQrUrl(ifaceId, peerId);
        this.wizardVPN.step = 4;

        // Wait for the async restart goroutine (PATCH with listenPort triggers
        // restart in a goroutine — the API returns before wg-quick finishes).
        await new Promise(r => setTimeout(r, 2500));
        await this.loadTunnelInterfaces();
        const created = this.tunnelInterfaces.find(i => i.id === ifaceId);
        if (created && !created.enabled) {
          this.wizardVPN.startWarning = `Interface ${ifaceId} was created but failed to start — UDP port ${wizardPort} may be in use. Go to Interfaces to start it manually or change the port.`;
        }
      } catch (err) {
        this.wizardVPN.error = err.message || 'Unknown error';
      } finally {
        this.wizardVPN.running = false;
      }
    },

wizardVPNReset() {
      this.wizardVPNInit();
    },

wizardVPNDownload() {
      this.downloadPeerConfig({
        id: this.wizardVPN.peerId,
        name: this.wizardVPN.peerName || 'My Device',
        interfaceId: this.wizardVPN.ifaceId,
      });
    },

    // ── Wizard: Cascade via WireGuard Uplink ─────────────────────────────────

wizardUplinkInit() {
      const w = this.wizardUplink;
      w.step = 1;
      w.confText = '';
      w.confFileName = '';
      w.preview = null;
      w.ifaceName = '';
      w.selectedIfaceIds = [];
      w.createSrcAlias = true;
      w.srcAliasName = '';
      w.showInlineCreate = false;
      w.inlineIfaceName = '';
      w.inlineIfaceAddr = '';
      w.inlineIfacePort = '';
      w.inlineIfaceCreating = false;
      w.dstType = 'all';
      w.dstCountries = [];
      w.dstASN = '';
      w.dstNegate = false;
      w.dstAliasName = '';
      w.mssClamp = true;
      w.fallback = 'drop';
      w.gatewayName = '';
      w.fwRuleName = '';
      w.natRuleName = '';
      w.gatewayMonitorIP = '';
      w.applying = false;
      w.steps = [];
      w.createdIfaceId = '';
      w.createdSrcAliasId = '';
      w.createdDstAliasId = '';
      w.createdGatewayId = '';
      w.createdFwRuleId = '';
      w.createdNatRuleId = '';
      w.done = false;
      w.fatalError = '';
    },

wizardUplinkOnFileSelect(event) {
      const file = event.target.files[0];
      if (!file) return;
      this.wizardUplink.confFileName = file.name.replace(/\.conf$/i, '');
      if (!this.wizardUplink.ifaceName) {
        this.wizardUplink.ifaceName = this.wizardUplink.confFileName;
      }
      const reader = new FileReader();
      reader.onload = (e) => {
        this.wizardUplink.confText = e.target.result;
        this.wizardUplinkPreview();
      };
      reader.readAsText(file);
    },

async wizardUplinkPreview() {
      const w = this.wizardUplink;
      w.preview = null;
      if (!w.confText.trim()) return;
      try {
        const res = await this.api.parseTunnelConf({ conf: w.confText });
        w.preview = res;
        const base = (w.ifaceName || w.confFileName || 'uplink').trim();
        if (!w.srcAliasName) w.srcAliasName = 'src-' + base;
        if (!w.dstAliasName) w.dstAliasName = 'dst-' + base;
        if (!w.gatewayName) w.gatewayName = 'gw-' + base;
        if (!w.fwRuleName) w.fwRuleName = 'pbr-' + base;
        if (!w.natRuleName) w.natRuleName = 'nat-' + base;
        if (!w.gatewayMonitorIP) w.gatewayMonitorIP = res.peerMonitorIP || '';
      } catch (e) {
        // invalid conf — show nothing
      }
    },

wizardUplinkOnNameChange() {
      const w = this.wizardUplink;
      const base = (w.ifaceName || 'uplink').trim();
      w.srcAliasName = 'src-' + base;
      w.dstAliasName = 'dst-' + base;
      w.gatewayName  = 'gw-'  + base;
      w.fwRuleName   = 'pbr-' + base;
      w.natRuleName  = 'nat-' + base;
    },

wizardUplinkClientIfaces() {
      return this.tunnelInterfaces.filter(i => !i.disableRoutes);
    },

wizardUplinkIfaceSubnet(iface) {
      if (!iface.address) return '';
      const parts = iface.address.split('/');
      if (parts.length !== 2) return iface.address;
      const ip = parts[0].split('.');
      const prefix = parseInt(parts[1], 10);
      ip[3] = '0';
      return ip.join('.') + '/' + (prefix <= 24 ? prefix : 24);
    },

wizardUplinkToggleIface(id) {
      const idx = this.wizardUplink.selectedIfaceIds.indexOf(id);
      if (idx === -1) this.wizardUplink.selectedIfaceIds.push(id);
      else this.wizardUplink.selectedIfaceIds.splice(idx, 1);
    },

wizardUplinkSelectedSubnets() {
      return this.wizardUplink.selectedIfaceIds.map(id => {
        const iface = this.tunnelInterfaces.find(i => i.id === id);
        return iface ? this.wizardUplinkIfaceSubnet(iface) : '';
      }).filter(Boolean);
    },

async wizardUplinkInlineCreate() {
      const w = this.wizardUplink;
      w.inlineIfaceCreating = true;
      try {
        const body = { protocol: w.inlineIfaceProto };
        if (w.inlineIfaceName.trim()) body.name = w.inlineIfaceName.trim();
        if (w.inlineIfaceAddr.trim()) body.address = w.inlineIfaceAddr.trim();
        if (w.inlineIfacePort) body.listenPort = parseInt(w.inlineIfacePort, 10);
        const res = await this.api.createTunnelInterface(body);
        const newId = (res.interface || res).id;
        if (newId) {
          await this.api.startTunnelInterface({ interfaceId: newId }).catch(() => {});
        }
        await this.loadTunnelInterfaces();
        if (newId) w.selectedIfaceIds.push(newId);
        w.showInlineCreate = false;
        w.inlineIfaceName = '';
        w.inlineIfaceAddr = '';
        w.inlineIfacePort = '';
      } catch (e) {
        this.showToast(e.message || 'Failed to create interface', 'error');
      } finally {
        w.inlineIfaceCreating = false;
      }
    },

    // ── Apply ─────────────────────────────────────────────────────────────────

wizardUplinkStepAdd(label) {
      this.wizardUplink.steps.push({ label, status: 'pending', detail: '' });
      return this.wizardUplink.steps.length - 1;
    },

wizardUplinkStepSet(idx, status, detail) {
      this.$set(this.wizardUplink.steps, idx, {
        ...this.wizardUplink.steps[idx],
        status,
        detail: detail || '',
      });
    },

async wizardUplinkApply() {
      const w = this.wizardUplink;
      w.applying = true;
      w.steps = [];
      w.done = false;
      w.fatalError = '';
      const base = (w.ifaceName || 'uplink').trim();

      // Step 1: Create uplink interface
      const s0 = this.wizardUplinkStepAdd('Creating uplink interface');
      this.wizardUplinkStepSet(s0, 'running');
      let ifaceId = '';
      try {
        const res = await this.api.importTunnelConf({ name: w.ifaceName.trim(), conf: w.confText });
        ifaceId = res.interface.id;
        w.createdIfaceId = ifaceId;
        await this.loadTunnelInterfaces();
        this.wizardUplinkStepSet(s0, 'ok', ifaceId + (res.started ? '' : ' (not started)'));
      } catch (e) {
        this.wizardUplinkStepSet(s0, 'error', e.message);
        w.fatalError = e.message;
        w.applying = false;
        return;
      }

      // Step 2: Propagate MTU to client interfaces (if uplink conf specifies MTU)
      const uplinkMTU = w.preview && w.preview.mtu ? parseInt(w.preview.mtu, 10) : 0;
      if (uplinkMTU > 0 && w.selectedIfaceIds.length > 0) {
        const s1b = this.wizardUplinkStepAdd('Setting client interface MTU');
        this.wizardUplinkStepSet(s1b, 'running');
        try {
          await Promise.all(w.selectedIfaceIds.map(id =>
            this.api.updateTunnelInterface({ interfaceId: id, mtu: uplinkMTU })
          ));
          this.wizardUplinkStepSet(s1b, 'ok', uplinkMTU + ' bytes');
        } catch (e) {
          this.wizardUplinkStepSet(s1b, 'warn', e.message + ' — continuing');
        }
      }

      // Step 3: Ping check
      const s1 = this.wizardUplinkStepAdd('Checking reachability');
      this.wizardUplinkStepSet(s1, 'running');
      try {
        const host = w.preview && w.preview.peerEndpoint
          ? w.preview.peerEndpoint.split(':')[0] : '';
        if (host) {
          const pr = await this.api.ping({ host, count: 3 });
          if (pr.reachable) {
            this.wizardUplinkStepSet(s1, 'ok', host + ' — ' + pr.latencyMs + 'ms');
          } else {
            this.wizardUplinkStepSet(s1, 'warn', host + ' not reachable — continuing');
          }
        } else {
          this.wizardUplinkStepSet(s1, 'warn', 'No endpoint — skipped');
        }
      } catch (e) {
        this.wizardUplinkStepSet(s1, 'warn', 'Ping failed — continuing');
      }

      // Step 3: Source alias
      if (w.createSrcAlias && w.selectedIfaceIds.length > 0) {
        const s2 = this.wizardUplinkStepAdd('Creating source alias');
        this.wizardUplinkStepSet(s2, 'running');
        try {
          const subnets = this.wizardUplinkSelectedSubnets();
          const res = await this.api.createAlias({
            name: w.srcAliasName || ('src-' + base),
            type: 'network',
            entries: subnets,
          });
          w.createdSrcAliasId = (res.alias || res).id || '';
          this.wizardUplinkStepSet(s2, 'ok', subnets.join(', '));
        } catch (e) {
          this.wizardUplinkStepSet(s2, 'warn', e.message + ' — continuing');
        }
      }

      // Step 4: Destination alias (GEO / AS)
      if (w.dstType !== 'all') {
        const s3 = this.wizardUplinkStepAdd('Creating destination alias');
        this.wizardUplinkStepSet(s3, 'running');
        try {
          const res = await this.api.createAlias({
            name: w.dstAliasName || ('dst-' + base),
            type: 'ipset',
            entries: [],
          });
          const aliasId = (res.alias || res).id || '';
          w.createdDstAliasId = aliasId;
          const genBody = w.dstType === 'geo'
            ? { country: w.dstCountries.join(','), asn: '', asnList: '' }
            : { country: '', asn: w.dstASN.trim(), asnList: '' };
          const genRes = await this.api.generateAlias({ id: aliasId, ...genBody });
          const jobId = genRes.jobId;
          let finished = false;
          while (!finished) {
            await new Promise(r => setTimeout(r, 1000));
            const st = await this.api.getAliasJobStatus({ id: aliasId, jobId });
            this.wizardUplinkStepSet(s3, 'running', (st.progress || 0) + '% loaded…');
            if (st.status === 'done') { finished = true; }
            if (st.status === 'error') throw new Error(st.error || 'Generation failed');
          }
          this.wizardUplinkStepSet(s3, 'ok', w.dstAliasName + ' created');
        } catch (e) {
          this.wizardUplinkStepSet(s3, 'warn', e.message + ' — continuing');
        }
      }

      // Step 5: MSS clamping
      if (w.mssClamp && ifaceId) {
        const s4 = this.wizardUplinkStepAdd('Enabling MSS clamping');
        this.wizardUplinkStepSet(s4, 'running');
        try {
          await this.api.updateTunnelInterface({ interfaceId: ifaceId, mss: -1 });
          this.wizardUplinkStepSet(s4, 'ok', 'Auto PMTU');
        } catch (e) {
          this.wizardUplinkStepSet(s4, 'warn', e.message + ' — continuing');
        }
      }

      // Step 6: Gateway
      const s5 = this.wizardUplinkStepAdd('Creating gateway');
      this.wizardUplinkStepSet(s5, 'running');
      let gatewayId = '';
      try {
        const res = await this.api.createGateway({
          name: w.gatewayName || ('gw-' + base),
          interface: ifaceId,
          gatewayIP: w.gatewayMonitorIP,
          monitorAddress: w.gatewayMonitorIP,
          monitor: true,
          monitorInterval: 5,
          latencyThreshold: 500,
          monitorRule: 'icmp_only',
        });
        gatewayId = (res.gateway || res).id || '';
        w.createdGatewayId = gatewayId;
        this.wizardUplinkStepSet(s5, 'ok', w.gatewayName);
      } catch (e) {
        this.wizardUplinkStepSet(s5, 'warn', e.message + ' — continuing');
      }

      // Step 7: Firewall PBR rule
      const s6 = this.wizardUplinkStepAdd('Creating firewall PBR rule');
      this.wizardUplinkStepSet(s6, 'running');
      try {
        const srcPart = w.createdSrcAliasId
          ? { type: 'alias', aliasId: w.createdSrcAliasId }
          : { type: 'any' };
        const dstPart = w.dstType === 'all'
          ? { type: 'any' }
          : w.createdDstAliasId
            ? { type: 'alias', aliasId: w.createdDstAliasId, invert: w.dstNegate }
            : { type: 'any' };
        const res = await this.api.createFirewallRule({
          name: w.fwRuleName || ('pbr-' + base),
          interface: 'any',
          protocol: 'any',
          source: srcPart,
          destination: dstPart,
          action: 'accept',
          gatewayId: gatewayId || undefined,
          fallbackToDefault: w.fallback === 'allow',
        });
        w.createdFwRuleId = (res.rule || res).id || '';
        await this.api.applyFirewallRules();
        this.wizardUplinkStepSet(s6, 'ok', w.fwRuleName);
      } catch (e) {
        this.wizardUplinkStepSet(s6, 'warn', e.message + ' — continuing');
      }

      // Step 8: NAT MASQUERADE
      const s7 = this.wizardUplinkStepAdd('Creating NAT MASQUERADE rule');
      this.wizardUplinkStepSet(s7, 'running');
      try {
        const natBody = {
          name: w.natRuleName || ('nat-' + base),
          outInterface: ifaceId,
          type: 'MASQUERADE',
        };
        if (w.createdSrcAliasId) natBody.sourceAliasId = w.createdSrcAliasId;
        const res = await this.api.createNatRule(natBody);
        w.createdNatRuleId = (res.rule || res).id || '';
        this.wizardUplinkStepSet(s7, 'ok', w.natRuleName);
      } catch (e) {
        this.wizardUplinkStepSet(s7, 'warn', e.message);
      }

      w.applying = false;
      w.done = true;
    },

    // ══════════════════════════════════════════════════════════════════════════
    // Wizard: Cascade ↔ Cascade S2S
    // ══════════════════════════════════════════════════════════════════════════

async wizardS2SInit() {
      const w = this.wizardS2S;
      w.step = 1;
      w.remoteId = ''; w.showAddRemote = false;
      w.addRemoteName = ''; w.addRemoteURL = ''; w.addRemoteMode = 'password';
      w.addRemoteUser = ''; w.addRemotePass = ''; w.addRemoteToken = '';
      w.addRemoteSkipTLS = false; w.addRemoteLoading = false;
      w.selectedIfaceIds = []; w.createSrcAlias = true; w.srcAliasName = '';
      w.dstType = 'all'; w.dstCountries = []; w.dstASN = ''; w.dstNegate = false; w.dstAliasName = '';
	  w.protocol = 'amneziawg-3.1'; w.mssClamp = true; w.fallback = 'drop';
      w.localIfaceName = ''; w.remoteIfaceName = ''; w.gatewayName = ''; w.fwRuleName = '';
      w.applying = false; w.steps = [];
      w.createdLocalIfaceId = ''; w.createdRemoteIfaceId = '';
      w.createdSrcAliasId = ''; w.createdDstAliasId = '';
      w.createdGatewayId = ''; w.createdFwRuleId = '';
      w.done = false; w.fatalError = '';
      try {
        const res = await this.api.call({ method: 'get', path: '/remotes' });
        w.remotes = res.remotes || res || [];
      } catch (e) {
        w.remotes = [];
      }
    },

wizardS2SOnNameChange() {
      const w = this.wizardS2S;
      const base = (w.localIfaceName || 's2s').trim();
      if (!w.remoteIfaceName || w.remoteIfaceName === 'remote-' + base) w.remoteIfaceName = 'remote-' + base;
      if (!w.srcAliasName || w.srcAliasName === 'src-' + base) w.srcAliasName = 'src-' + base;
      if (!w.dstAliasName || w.dstAliasName === 'dst-' + base) w.dstAliasName = 'dst-' + base;
      if (!w.gatewayName || w.gatewayName === 'gw-' + base) w.gatewayName = 'gw-' + base;
      if (!w.fwRuleName || w.fwRuleName === 'pbr-' + base) w.fwRuleName = 'pbr-' + base;
    },

wizardS2SAutoNames() {
      const w = this.wizardS2S;
      const remote = w.remotes.find(r => r.id === w.remoteId);
      const base = (remote ? remote.name.toLowerCase().replace(/[^a-z0-9]/g, '-') : 'cascade') + '-s2s';
      if (!w.localIfaceName) w.localIfaceName = base;
      if (!w.remoteIfaceName) w.remoteIfaceName = 'remote-' + base;
      if (!w.srcAliasName) w.srcAliasName = 'src-' + base;
      if (!w.dstAliasName) w.dstAliasName = 'dst-' + base;
      if (!w.gatewayName) w.gatewayName = 'gw-' + base;
      if (!w.fwRuleName) w.fwRuleName = 'pbr-' + base;
    },

async wizardS2SAddRemote() {
      const w = this.wizardS2S;
      w.addRemoteLoading = true;
      try {
        const body = {
          name: w.addRemoteName.trim(),
          url: w.addRemoteURL.trim(),
          skipTLS: w.addRemoteSkipTLS,
        };
        if (w.addRemoteMode === 'password') {
          body.username = w.addRemoteUser.trim();
          body.password = w.addRemotePass;
        } else {
          body.token = w.addRemoteToken.trim();
        }
        const res = await this.api.call({ method: 'post', path: '/remotes', body });
        const added = res.remote || res;
        w.remotes.push(added);
        w.remoteId = added.id;
        w.showAddRemote = false;
        w.addRemoteName = ''; w.addRemoteURL = ''; w.addRemoteUser = ''; w.addRemotePass = ''; w.addRemoteToken = '';
      } catch (e) {
        this.showToast(e.message || 'Failed to add remote', 'error');
      } finally {
        w.addRemoteLoading = false;
      }
    },

wizardS2SClientIfaces() {
      return (this.tunnelInterfaces || []).filter(i => !i.disableRoutes);
    },

wizardS2SIfaceSubnet(iface) {
      if (!iface || !iface.address) return '';
      const parts = iface.address.split('/');
      if (parts.length < 2) return iface.address;
      const ipParts = parts[0].split('.');
      ipParts[3] = '0';
      return ipParts.join('.') + '/' + parts[1];
    },

wizardS2SToggleIface(id) {
      const idx = this.wizardS2S.selectedIfaceIds.indexOf(id);
      if (idx === -1) this.wizardS2S.selectedIfaceIds.push(id);
      else this.wizardS2S.selectedIfaceIds.splice(idx, 1);
    },

wizardS2SSelectedSubnets() {
      return this.wizardS2S.selectedIfaceIds.map(id => {
        const iface = (this.tunnelInterfaces || []).find(i => i.id === id);
        return iface ? this.wizardS2SIfaceSubnet(iface) : '';
      }).filter(Boolean);
    },

wizardS2SFreeSubnet(remoteAddresses) {
      const localUsed  = (this.tunnelInterfaces || []).map(i => i.address).filter(Boolean);
      const remoteUsed = remoteAddresses || [];
      const used = [...localUsed, ...remoteUsed];
      for (let base = 0; base < 256; base += 4) {
        const localIP  = `10.255.255.${base + 1}`;
        const remoteIP = `10.255.255.${base + 2}`;
        const inUse = used.some(addr =>
          addr.startsWith(localIP + '/') || addr.startsWith(localIP + ' ') || addr === localIP ||
          addr.startsWith(remoteIP + '/') || addr.startsWith(remoteIP + ' ') || addr === remoteIP
        );
        if (!inUse) return { localAddr: localIP + '/30', remoteAddr: remoteIP + '/30', localIP, remoteIP };
      }
      return null;
    },

wizardS2SStepAdd(label) {
      this.wizardS2S.steps.push({ label, status: 'pending', detail: '' });
      return this.wizardS2S.steps.length - 1;
    },

wizardS2SStepSet(idx, status, detail) {
      this.$set(this.wizardS2S.steps, idx, {
        ...this.wizardS2S.steps[idx],
        status,
        detail: detail || '',
      });
    },

async wizardS2SApply() {
      const w = this.wizardS2S;
      w.applying = true; w.steps = []; w.done = false; w.fatalError = '';
      const rid = w.remoteId;

      // Step 0: Pre-flight — verify source subnets are not already routed/NATed on remote.
      // Prevents partial execution when two servers share the same client subnets
      // (e.g. two Cascade nodes routing to the same exit via the same remote).
      if (w.selectedIfaceIds.length > 0) {
        const sp = this.wizardS2SStepAdd('Checking source subnet availability on remote');
        this.wizardS2SStepSet(sp, 'running');

        const localSubnets = this.wizardS2SSelectedSubnets();
        this.wizardS2SStepSet(sp, 'running', `Local subnets: ${localSubnets.join(', ')}`);

        // Fetch NAT rules and static routes from remote independently so we can report
        // which fetch failed; do NOT silently swallow errors as "no subnets".
        let natRules = null, remoteRoutes = null, fetchErr = '';
        try { natRules = (await this.api.remoteCall({ remoteId: rid, method: 'get', path: '/nat/rules' })).rules || []; }
        catch (e) { fetchErr += `NAT rules: ${e.message}; `; }
        try { remoteRoutes = (await this.api.remoteCall({ remoteId: rid, method: 'get', path: '/routing/routes' })).routes || []; }
        catch (e) { fetchErr += `Routes: ${e.message}; `; }

        if (natRules === null && remoteRoutes === null) {
          // Both fetches failed — cannot verify, block the wizard.
          const msg = `Cannot reach remote to verify subnet availability: ${fetchErr.trimEnd()}`;
          this.wizardS2SStepSet(sp, 'error', msg);
          w.fatalError = msg; w.applying = false; return;
        }

        // Collect all subnets already known on remote: NAT sources + static route destinations
        const remoteSubnets = [];
        for (const rule of (natRules || [])) {
          if (rule.source) remoteSubnets.push(rule.source);
        }
        for (const route of (remoteRoutes || [])) {
          if (route.destination) remoteSubnets.push(route.destination);
        }

        if (fetchErr) {
          // Partial fetch — note it but continue with what we have
          this.wizardS2SStepSet(sp, 'running', `Partial data (${fetchErr.trimEnd()}) — ${remoteSubnets.length} subnets found`);
        }

        const conflicts = localSubnets.filter(s => remoteSubnets.includes(s));
        if (conflicts.length > 0) {
          const msg = `Source subnet(s) already present on remote: ${conflicts.join(', ')}. Another server may already route these subnets through this exit.`;
          this.wizardS2SStepSet(sp, 'error', msg);
          w.fatalError = msg; w.applying = false; return;
        }

        this.wizardS2SStepSet(sp, 'ok', `No conflicts — remote has ${remoteSubnets.length} existing subnet entries${fetchErr ? ' (partial data)' : ''}`);
      }

      // Step 1: Find free /30 — check both local and remote interfaces
      const s0 = this.wizardS2SStepAdd('Allocating S2S subnet');
      this.wizardS2SStepSet(s0, 'running');
      let remoteAddresses = [];
      try {
        const remoteIfaces = await this.api.remoteCall({ remoteId: rid, method: 'get', path: '/tunnel-interfaces' });
        remoteAddresses = ((remoteIfaces.interfaces || remoteIfaces || []).map(i => i.address)).filter(Boolean);
        this.wizardS2SStepSet(s0, 'running', `Local: ${(this.tunnelInterfaces||[]).length} ifaces, remote: ${remoteAddresses.length} ifaces`);
      } catch (e) {
        this.wizardS2SStepSet(s0, 'running', 'Could not fetch remote interfaces — checking local only');
      }
      const subnet = this.wizardS2SFreeSubnet(remoteAddresses);
      if (!subnet) {
        this.wizardS2SStepSet(s0, 'error', '10.255.255.0/24 exhausted on local or remote');
        w.fatalError = 'No free /30 in 10.255.255.0/24'; w.applying = false; return;
      }
      this.wizardS2SStepSet(s0, 'ok', `${subnet.localAddr} ↔ ${subnet.remoteAddr}`);

      // Step 2: Create local S2S interface
      const s1 = this.wizardS2SStepAdd('Creating local S2S interface');
      this.wizardS2SStepSet(s1, 'running');
      let localIfaceId = '';
      let localSettings = null;
      try {
        const body = { name: w.localIfaceName, address: subnet.localAddr, disableRoutes: true, protocol: w.protocol };
        const res = await this.api.createTunnelInterface(body);
        const iface = res.interface || res;
        localIfaceId = iface.id || '';
        localSettings = iface.settings || null;
        w.createdLocalIfaceId = localIfaceId;
        await this.api.startTunnelInterface({ interfaceId: localIfaceId }).catch(() => {});
        await this.loadTunnelInterfaces();
        this.wizardS2SStepSet(s1, 'ok', w.localIfaceName);
      } catch (e) {
        this.wizardS2SStepSet(s1, 'error', e.message);
        w.fatalError = e.message; w.applying = false; return;
      }

      // Step 3: Create remote S2S interface
      const s2 = this.wizardS2SStepAdd('Creating remote S2S interface');
      this.wizardS2SStepSet(s2, 'running');
      let remoteIfaceId = '';
      try {
        const body = { name: w.remoteIfaceName, address: subnet.remoteAddr, disableRoutes: true, protocol: w.protocol };
		// Copy the exact shared settings, including the AWG 3.1 header key.
		if (w.protocol.startsWith('amneziawg-') && localSettings) body.settings = localSettings;
        const res = await this.api.remoteCall({ remoteId: rid, method: 'post', path: '/tunnel-interfaces', body });
        const remoteIface = res.interface || res;
        remoteIfaceId = remoteIface.id || '';
        w.createdRemoteIfaceId = remoteIfaceId;
        await this.api.remoteCall({ remoteId: rid, method: 'post', path: `/tunnel-interfaces/${remoteIfaceId}/start`, body: {} }).catch(() => {});
        this.wizardS2SStepSet(s2, 'ok', w.remoteIfaceName);
      } catch (e) {
        this.wizardS2SStepSet(s2, 'error', e.message);
        w.fatalError = e.message; w.applying = false; return;
      }

      // Step 4: Exchange WireGuard params
      // Order matters for PSK sync:
      //   1. Export local (no PSK yet)  → import into remote  → remote generates PSK
      //   2. Export remote (PSK present) → import into local   → local receives same PSK
      // Reversing the export order causes both sides to generate independent PSKs → handshake failure.
      const s3 = this.wizardS2SStepAdd('Exchanging WireGuard keys');
      this.wizardS2SStepSet(s3, 'running');
      try {
        // export-params for disableRoutes=true already returns allowedIPs=0.0.0.0/0 — keep as-is.
        const localParams = await this.api.call({ method: 'get', path: `/tunnel-interfaces/${localIfaceId}/export-params` });
        await this.api.remoteCall({ remoteId: rid, method: 'post', path: `/tunnel-interfaces/${remoteIfaceId}/peers/import-json`, body: localParams });
        // Remote now has an interconnect peer with a generated PSK — re-export to get it.
        const remoteParams = await this.api.remoteCall({ remoteId: rid, method: 'get', path: `/tunnel-interfaces/${remoteIfaceId}/export-params` });
        await this.api.call({ method: 'post', path: `/tunnel-interfaces/${localIfaceId}/peers/import-json`, body: remoteParams });
        this.wizardS2SStepSet(s3, 'ok', 'Keys exchanged');
      } catch (e) {
        this.wizardS2SStepSet(s3, 'error', e.message);
        w.fatalError = e.message; w.applying = false; return;
      }

      // Step 6: Source alias
      if (w.createSrcAlias && w.selectedIfaceIds.length > 0) {
        const s4 = this.wizardS2SStepAdd('Creating source alias');
        this.wizardS2SStepSet(s4, 'running');
        try {
          const subnets = this.wizardS2SSelectedSubnets();
          const res = await this.api.createAlias({ name: w.srcAliasName, type: 'network', entries: subnets });
          w.createdSrcAliasId = (res.alias || res).id || '';
          this.wizardS2SStepSet(s4, 'ok', subnets.join(', '));
        } catch (e) {
          this.wizardS2SStepSet(s4, 'warn', e.message + ' — continuing');
        }
      }

      // Step 6: Destination alias (GEO/AS)
      if (w.dstType !== 'all') {
        const s5 = this.wizardS2SStepAdd('Creating destination alias');
        this.wizardS2SStepSet(s5, 'running');
        try {
          const res = await this.api.createAlias({ name: w.dstAliasName, type: 'ipset', entries: [] });
          const aliasId = (res.alias || res).id || '';
          w.createdDstAliasId = aliasId;
          const genBody = w.dstType === 'geo'
            ? { country: w.dstCountries.join(','), asn: '', asnList: '' }
            : { country: '', asn: w.dstASN.trim(), asnList: '' };
          const genRes = await this.api.generateAlias({ id: aliasId, ...genBody });
          const jobId = genRes.jobId;
          let finished = false;
          while (!finished) {
            await new Promise(r => setTimeout(r, 1000));
            const st = await this.api.getAliasJobStatus({ id: aliasId, jobId });
            this.wizardS2SStepSet(s5, 'running', (st.progress || 0) + '% loaded…');
            if (st.status === 'done') { finished = true; }
            if (st.status === 'error') throw new Error(st.error || 'Generation failed');
          }
          this.wizardS2SStepSet(s5, 'ok', w.dstAliasName + ' created');
        } catch (e) {
          this.wizardS2SStepSet(s5, 'warn', e.message + ' — continuing');
        }
      }

      // Step 7: MSS clamping on local S2S interface
      if (w.mssClamp && localIfaceId) {
        const s6 = this.wizardS2SStepAdd('Enabling MSS clamping');
        this.wizardS2SStepSet(s6, 'running');
        try {
          await this.api.updateTunnelInterface({ interfaceId: localIfaceId, mss: -1 });
          this.wizardS2SStepSet(s6, 'ok', 'Auto PMTU');
        } catch (e) {
          this.wizardS2SStepSet(s6, 'warn', e.message + ' — continuing');
        }
      }

      // Step 8: Local gateway
      const s7 = this.wizardS2SStepAdd('Creating gateway');
      this.wizardS2SStepSet(s7, 'running');
      let gatewayId = '';
      try {
        const res = await this.api.createGateway({
          name: w.gatewayName,
          interface: localIfaceId,
          gatewayIP: subnet.remoteIP,
          monitorAddress: subnet.remoteIP,
          monitor: true,
          monitorInterval: 5,
          latencyThreshold: 500,
          monitorRule: 'icmp_only',
        });
        gatewayId = (res.gateway || res).id || '';
        w.createdGatewayId = gatewayId;
        this.wizardS2SStepSet(s7, 'ok', w.gatewayName);
      } catch (e) {
        this.wizardS2SStepSet(s7, 'warn', e.message + ' — continuing');
      }

      // Step 9: Local PBR firewall rule
      const s8 = this.wizardS2SStepAdd('Creating firewall PBR rule');
      this.wizardS2SStepSet(s8, 'running');
      try {
        const srcPart = w.createdSrcAliasId ? { type: 'alias', aliasId: w.createdSrcAliasId } : { type: 'any' };
        const dstPart = w.dstType === 'all' ? { type: 'any' }
          : w.createdDstAliasId ? { type: 'alias', aliasId: w.createdDstAliasId, invert: w.dstNegate } : { type: 'any' };
        const res = await this.api.createFirewallRule({
          name: w.fwRuleName,
          interface: 'any',
          protocol: 'any',
          source: srcPart,
          destination: dstPart,
          action: 'accept',
          gatewayId: gatewayId || undefined,
          fallbackToDefault: w.fallback === 'allow',
        });
        w.createdFwRuleId = (res.rule || res).id || '';
        await this.api.applyFirewallRules();
        this.wizardS2SStepSet(s8, 'ok', w.fwRuleName);
      } catch (e) {
        this.wizardS2SStepSet(s8, 'warn', e.message + ' — continuing');
      }

      // Step 10: Remote return route (client subnets → local S2S IP)
      const s9 = this.wizardS2SStepAdd('Adding return routes on remote');
      this.wizardS2SStepSet(s9, 'running');
      try {
        const subnets = this.wizardS2SSelectedSubnets();
        if (subnets.length > 0) {
          await Promise.all(subnets.map(cidr =>
            this.api.remoteCall({ remoteId: rid, method: 'post', path: '/routing/routes', body: {
              destination: cidr,
              gateway: subnet.localIP,
              dev: remoteIfaceId,
              enabled: true,
            }})
          ));
          this.wizardS2SStepSet(s9, 'ok', subnets.join(', '));
        } else {
          this.wizardS2SStepSet(s9, 'warn', 'No subnets selected — skipped');
        }
      } catch (e) {
        this.wizardS2SStepSet(s9, 'warn', e.message + ' — continuing');
      }

      // Step 11: Remote NAT MASQUERADE on system interface
      const s10 = this.wizardS2SStepAdd('Adding NAT on remote');
      this.wizardS2SStepSet(s10, 'running');
      try {
        const ifacesRes = await this.api.remoteCall({ remoteId: rid, method: 'get', path: '/system/interfaces' });
        const ifaces = ifacesRes.interfaces || [];
        const sysIface = ifaces.find(i => !i.name.startsWith('wg') && !i.name.startsWith('awg') && i.name !== 'lo');
        if (!sysIface) throw new Error('No system interface found on remote');
        const natBody = { name: 'nat-' + w.remoteIfaceName, outInterface: sysIface.name, type: 'MASQUERADE' };
        if (w.createdSrcAliasId) {
          // Create matching alias on remote too
          const srcSubnets = this.wizardS2SSelectedSubnets();
          const remoteAlias = await this.api.remoteCall({ remoteId: rid, method: 'post', path: '/aliases', body: {
            name: w.srcAliasName + '-remote', type: 'network', entries: srcSubnets,
          }});
          const remoteAliasId = (remoteAlias.alias || remoteAlias).id || '';
          if (remoteAliasId) natBody.sourceAliasId = remoteAliasId;
        }
        await this.api.remoteCall({ remoteId: rid, method: 'post', path: '/nat/rules', body: natBody });
        this.wizardS2SStepSet(s10, 'ok', sysIface.name);
      } catch (e) {
        this.wizardS2SStepSet(s10, 'warn', e.message + ' — continuing');
      }

      w.applying = false;
      w.done = true;
    },

};
