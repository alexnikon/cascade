/**
 * Peers feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const peerMethods = {
async createQuickPeer() {
      if (!this.activeInterfaceId) return;
      const name = this.peerCreateName;
      if (!name) return;

      const expiredAt = this.expiryDateTimeToUTC(this.peerCreateExpiredDate);
      if (this.peerCreateExpiredDate && !expiredAt) {
        this.showToast('Invalid expiry date and time', 'error');
        return;
      }
      if (this.peerMutationInFlight) return;
      this.peerMutationInFlight = true;

      try {
        const payload = {
          name,
          autoAllocateIP: true,
          generateKeys: true,
          expiredAt: expiredAt || undefined,
          groupId: this.peerCreateGroupId || this.defaultGroupId() || undefined,
        };

        const res = await this.api.createTunnelInterfacePeer({
          interfaceId: this.activeInterfaceId,
          ...payload,
        });

        const peerId = res.peer && res.peer.id;
        const showQR = this.peerCreateShowQR;
        this.showQuickPeerCreate = false;
        this.peerCreateName = '';
        this.peerCreateExpiredDate = '';
        this.peerCreateShowQR = false;
        this.inlineGroupShowQuick = false;
        this.inlineGroupInput = '';

        await this.refreshPeers();
        await this.loadTunnelInterfaces();
        this.loadClientGroups();
        this.loadAliases();

        if (showQR && peerId) {
          this.qrcode = this.peerQrUrl(this.activeInterfaceId, peerId);
        } else {
          this.showToast('Client created!');
        }
      } catch (err) {
        console.error('Failed to create peer:', err);
        this.showToast(`Failed: ${err.message}`, 'error');
      } finally {
        this.peerMutationInFlight = false;
      }
    },

    // ========================================================================
    // Peer management methods (admin-tunnel style)
    // ========================================================================

    // Returns the correct interfaceId for a peer action, works both in per-interface view and dashboard

_peerIfaceId(peer) {
      return (peer && peer.interfaceId) || this.activeInterfaceId;
    },

    // Extract IP from runtimeEndpoint "IP:port" string (IPv4 and IPv6)

peerQrUrl(interfaceId, peerId) {
      const base = this.activeRemoteId
        ? `./api/remotes/${this.activeRemoteId}/proxy/tunnel-interfaces/${interfaceId}/peers/${peerId}/qrcode.svg`
        : `./api/tunnel-interfaces/${interfaceId}/peers/${peerId}/qrcode.svg`;
      return base;
    },

peerPublicIP(endpoint) {
      if (!endpoint) return '';
      if (endpoint.startsWith('[')) {
        // IPv6: [::1]:51820 → ::1
        return endpoint.slice(1, endpoint.indexOf(']'));
      }
      // IPv4: 1.2.3.4:51820 → 1.2.3.4
      return endpoint.split(':')[0];
    },

    // Refresh peers: if an interface tab is selected, refresh that interface; otherwise refresh all (dashboard)

async _refreshPeersOrAll(opts = {}) {
      if (this.activeInterfaceId) {
        await this.refreshPeers(opts);
      } else {
        await this.refreshAllPeers(opts);
      }
    },

async enablePeer(peer) {
      try {
        await this.api.enablePeer({ interfaceId: this._peerIfaceId(peer), peerId: peer.id });
        await this._refreshPeersOrAll();
      } catch (err) {
        this.showToast(err.message || err.toString(), 'error');
      }
    },

async disablePeer(peer) {
      try {
        await this.api.disablePeer({ interfaceId: this._peerIfaceId(peer), peerId: peer.id });
        await this._refreshPeersOrAll();
      } catch (err) {
        this.showToast(err.message || err.toString(), 'error');
      }
    },

async updatePeerName(peer, name) {
      try {
        await this.api.updatePeerName({ interfaceId: this._peerIfaceId(peer), peerId: peer.id, name });
        await this._refreshPeersOrAll();
      } catch (err) {
        this.showToast(err.message || err.toString(), 'error');
      }
    },

async updatePeerAddress(peer, address) {
      try {
        await this.api.updatePeerAddress({ interfaceId: this._peerIfaceId(peer), peerId: peer.id, address });
        await this._refreshPeersOrAll();
      } catch (err) {
        this.showToast(err.message || err.toString(), 'error');
      }
    },

async updatePeerExpireDate(peer, expireDate) {
      try {
        await this.api.updatePeerExpireDate({ interfaceId: this._peerIfaceId(peer), peerId: peer.id, expireDate });
        await this._refreshPeersOrAll();
      } catch (err) {
        this.showToast(err.message || err.toString(), 'error');
      }
    },

async showPeerOneTimeLink(peer) {
      try {
        const res = await this.api.generatePeerOneTimeLink({ interfaceId: this._peerIfaceId(peer), peerId: peer.id });
        const token = (res.peer || {}).oneTimeLink;
        if (token) {
          const url = `${location.protocol}//${location.host}/cnf/${token}`;
          await navigator.clipboard.writeText(url);
          this.showToast('One-time link copied to clipboard', 'success');
        }
        await this._refreshPeersOrAll();
      } catch (err) {
        this.showToast(err.message || err.toString(), 'error');
      }
    },

async confirmDeletePeer() {
      if (!this.peerDelete) return;
      const label = this.peerDelete.peerType === 'interconnect' ? 'Peer' : 'Client';
      try {
        await this.api.deleteTunnelInterfacePeer({
          interfaceId: this._peerIfaceId(this.peerDelete),
          peerId: this.peerDelete.id,
        });
        this.peerDelete = null;
        await this._refreshPeersOrAll();
        await this.loadTunnelInterfaces();
        if (label === 'Client') {
          this.loadClientGroups();
          this.loadAliases();
        }
        this.showToast(`${label} deleted!`);
      } catch (err) {
        this.showToast(err.message || err.toString(), 'error');
      }
    },

openPeerEdit(peer) {
      this.peerEditForm = {
        _peer: peer,
        name: peer.name || '',
        persistentKeepalive: peer.persistentKeepalive || 0,
        endpoint: peer.endpoint || '',
        allowedIPs: peer.allowedIPs || '',
        clientAllowedIPs: peer.clientAllowedIPs || '',
        rateDown: peer.rateDown ? peer.rateDown / 1000 : 0,
        rateUp:   peer.rateUp   ? peer.rateUp   / 1000 : 0,
        groupId: peer.groupId || (peer.peerType === 'client' ? this.defaultGroupId() : ''),
        expiredAt: this.expiryDateTimeForInput(peer.expiredAt),
      };
      this.showPeerEditModal = true;
    },

expiryDateTimeToUTC(value) {
      if (!value) return '';
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? '' : date.toISOString();
    },

expiryDateTimeForInput(value) {
      if (!value) return '';
      const date = value instanceof Date ? value : new Date(value);
      if (Number.isNaN(date.getTime())) return '';
      const pad = number => String(number).padStart(2, '0');
      return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
    },

    // Format kbps rate for display. 0 = unlimited.

formatRate(kbps) {
      if (!kbps || kbps <= 0) return '';
      if (kbps >= 1000) return (kbps / 1000).toFixed(kbps % 1000 === 0 ? 0 : 1) + 'M';
      return kbps + 'K';
    },

    // Return the client group name for a given groupId. Returns '' if not found.

peerGroupName(groupId) {
      if (!groupId) return '';
      const g = this.clientGroups.find(g => g.id === groupId);
      return g ? g.name : '';
    },

    // Returns effective rate limits for a peer: individual limits take precedence,
    // otherwise falls back to the peer's group limits.
    // Returns { rateDown, rateUp, fromGroup } — fromGroup=true when limits come from group.

peerEffectiveRate(peer) {
      if (peer.rateDown > 0 || peer.rateUp > 0) {
        return { rateDown: peer.rateDown, rateUp: peer.rateUp, fromGroup: false };
      }
      if (peer.groupId) {
        const g = this.clientGroups.find(g => g.id === peer.groupId);
        if (g && (g.rateDown > 0 || g.rateUp > 0)) {
          return { rateDown: g.rateDown, rateUp: g.rateUp, fromGroup: true };
        }
      }
      return { rateDown: 0, rateUp: 0, fromGroup: false };
    },

async savePeerEdit() {
      const peer = this.peerEditForm._peer;
      if (!peer) return;
      if (this.peerMutationInFlight) return;
      const isInterconnect = peer.peerType === 'interconnect';
      const updates = {
        name: this.peerEditForm.name,
        persistentKeepalive: Number(this.peerEditForm.persistentKeepalive) || 0,
      };
      if (isInterconnect) {
        updates.endpoint = this.peerEditForm.endpoint;
        updates.allowedIPs = this.peerEditForm.allowedIPs;
      } else {
        const expiredAt = this.expiryDateTimeToUTC(this.peerEditForm.expiredAt);
        if (this.peerEditForm.expiredAt && !expiredAt) {
          this.showToast('Invalid expiry date and time', 'error');
          return;
        }
        updates.clientAllowedIPs = this.peerEditForm.clientAllowedIPs;
        updates.rateDown = Math.round((Number(this.peerEditForm.rateDown) || 0) * 1000);
        updates.rateUp   = Math.round((Number(this.peerEditForm.rateUp)   || 0) * 1000);
        updates.groupId  = this.peerEditForm.groupId || '';
        updates.expiredAt = expiredAt;
      }
      this.peerMutationInFlight = true;
      try {
        await this.api.updateTunnelInterfacePeer({
          interfaceId: this._peerIfaceId(peer),
          peerId: peer.id,
          ...updates,
        });
        this.showPeerEditModal = false;
        this.peerEditForm._peer = null;
        await this._refreshPeersOrAll();
        if (!isInterconnect) {
          this.loadClientGroups();
          this.loadAliases();
        }
        this.showToast(isInterconnect ? 'Peer updated' : 'Client updated', 'success');
      } catch (err) {
        this.showToast(err.message || err.toString(), 'error');
      } finally {
        this.peerMutationInFlight = false;
      }
    },

    /**
     * Refresh peers with transfer stats (called periodically like admin tunnel's refresh()).
     */
};
