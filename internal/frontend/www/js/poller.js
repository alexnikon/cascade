/**
 * Poller feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const pollerMethods = {
startResourcePoller() {
      if (!this._resourceVisibilityHandler) {
        this._resourceVisibilityHandler = () => {
          if (document.hidden) {
            this.stopResourcePoller();
            return;
          }
          this.startResourcePoller();
          this.resourcePollTick().catch(console.error);
        };
        document.addEventListener('visibilitychange', this._resourceVisibilityHandler);
      }
      if (document.hidden || this.resourcePoller) return;
      this.resourcePoller = setInterval(() => {
        this.resourcePollTick().catch(console.error);
      }, 5000);
    },

stopResourcePoller() {
      if (!this.resourcePoller) return;
      clearInterval(this.resourcePoller);
      this.resourcePoller = null;
    },

async resourcePollTick() {
      if (!this.authenticated || document.hidden) return;
      if (this.resourcePollPromise) {
        this.resourcePollSkipped += 1;
        return this.resourcePollPromise;
      }
      this.resourcePollPromise = (async () => {
        await this.refresh();
        if (this.activePage === 'interfaces') {
          if (this.activeInterfaceId) {
            await this.refreshPeers({ scheduled: true });
          } else {
            await this.refreshAllPeers({ scheduled: true });
          }
        } else if (this.activePage === 'gateways') {
          await this.refreshGateways();
        } else if (this.activePage === 'dashboard') {
          await Promise.all([
            this.refreshGateways(),
            this.refreshAllPeers({ scheduled: true }),
          ]);
        }
      })();
      try {
        return await this.resourcePollPromise;
      } finally {
        this.resourcePollPromise = null;
      }
    },

async refreshPeers(options = {}) {
      const interfaceId = this.activeInterfaceId;
      if (!interfaceId) return;
      if (this.refreshPeersPromise && this.refreshPeersPromiseKey === interfaceId) {
        this.resourcePollSkipped += 1;
        return this.refreshPeersPromise;
      }

      const promise = this._refreshPeersNow(options, interfaceId);
      this.refreshPeersPromise = promise;
      this.refreshPeersPromiseKey = interfaceId;
      try {
        return await promise;
      } finally {
        if (this.refreshPeersPromise === promise) {
          this.refreshPeersPromise = null;
          this.refreshPeersPromiseKey = null;
        }
      }
    },

async _refreshPeersNow(_options = {}, interfaceId = this.activeInterfaceId) {
      if (!this.authenticated || !interfaceId) return;

      const seq = (this.peerRefreshSeq || 0) + 1;
      this.peerRefreshSeq = seq;
      this.selectedPeersLoading = true;
      this.selectedPeersError = '';

      try {
        const res = await this.api.getTunnelInterfacePeers({ interfaceId });
        const peers = (res.peers || []).map(peer => {
          // Tag with interfaceId so actions work from dashboard too
          peer.interfaceId = interfaceId;

          // Parse dates
          peer.createdAt = peer.createdAt ? new Date(peer.createdAt) : null;
          peer.updatedAt = peer.updatedAt ? new Date(peer.updatedAt) : null;
          peer.expiredAt = peer.expiredAt ? new Date(peer.expiredAt) : null;
          peer.latestHandshakeAt = peer.latestHandshakeAt ? new Date(peer.latestHandshakeAt) : null;

          // Avatar
          if (peer.name && this.avatarSettings.dicebear) {
            peer.avatar = `https://api.dicebear.com/9.x/${this.avatarSettings.dicebear}/svg?seed=${sha256(peer.name.toLowerCase().trim())}`;
          }

          // Transfer stats persistence for current-rate display.
          if (!this.peersPersist[peer.id]) {
            this.peersPersist[peer.id] = {};
            this.peersPersist[peer.id].transferRxPrevious = peer.transferRx || 0;
            this.peersPersist[peer.id].transferTxPrevious = peer.transferTx || 0;
          }

          const pp = this.peersPersist[peer.id];
          pp.transferRxCurrent = (peer.transferRx || 0) - pp.transferRxPrevious;
          pp.transferRxPrevious = peer.transferRx || 0;
          pp.transferTxCurrent = (peer.transferTx || 0) - pp.transferTxPrevious;
          pp.transferTxPrevious = peer.transferTx || 0;

          peer.transferTxCurrent = pp.transferTxCurrent;
          peer.transferRxCurrent = pp.transferRxCurrent;
          peer.hoverTx = pp.hoverTx;
          peer.hoverRx = pp.hoverRx;

          return peer;
        });

        if (seq !== this.peerRefreshSeq || interfaceId !== this.activeInterfaceId) return;
        this.selectedInterfacePeers = peers;
        this.selectedPeersLoaded = true;
      } catch (err) {
        if (seq !== this.peerRefreshSeq || interfaceId !== this.activeInterfaceId) return;
        this.selectedPeersError = 'Failed to load peers.';
        console.error('refreshPeers failed:', err);
      } finally {
        if (seq === this.peerRefreshSeq && interfaceId === this.activeInterfaceId) {
          this.selectedPeersLoading = false;
        }
      }
    },

    /**
     * Dashboard mode: load peers from ALL interfaces into this.allPeers.
     * Each peer gets peer.interfaceId and peer.interfaceName set.
     */

async refreshAllPeers(options = {}) {
      const remoteKey = this.activeRemoteId || 'local';
      if (this.refreshAllPeersPromise && this.refreshAllPeersPromiseKey === remoteKey) {
        this.resourcePollSkipped += 1;
        return this.refreshAllPeersPromise;
      }

      const promise = this._refreshAllPeersNow(options, remoteKey);
      this.refreshAllPeersPromise = promise;
      this.refreshAllPeersPromiseKey = remoteKey;
      try {
        return await promise;
      } finally {
        if (this.refreshAllPeersPromise === promise) {
          this.refreshAllPeersPromise = null;
          this.refreshAllPeersPromiseKey = null;
        }
      }
    },

async _refreshAllPeersNow(_options = {}, remoteKey = this.activeRemoteId || 'local') {
      if (!this.authenticated) return;

      const seq = (this.allPeerRefreshSeq || 0) + 1;
      this.allPeerRefreshSeq = seq;
      this.allPeersLoading = true;
      this.allPeersError = '';
      try {
        const res = await this.api.getAllTunnelPeers();
        const all = (res.peers || []).map(peer => {
          peer.interfaceName = peer.interfaceName || peer.interfaceId;

          peer.createdAt = peer.createdAt ? new Date(peer.createdAt) : null;
          peer.updatedAt = peer.updatedAt ? new Date(peer.updatedAt) : null;
          peer.expiredAt = peer.expiredAt ? new Date(peer.expiredAt) : null;
          peer.latestHandshakeAt = peer.latestHandshakeAt ? new Date(peer.latestHandshakeAt) : null;

          if (peer.name && this.avatarSettings.dicebear) {
            peer.avatar = `https://api.dicebear.com/9.x/${this.avatarSettings.dicebear}/svg?seed=${sha256(peer.name.toLowerCase().trim())}`;
          }

          if (!this.peersPersist[peer.id]) {
            this.peersPersist[peer.id] = {};
            this.peersPersist[peer.id].transferRxPrevious = peer.transferRx || 0;
            this.peersPersist[peer.id].transferTxPrevious = peer.transferTx || 0;
          }
          const pp = this.peersPersist[peer.id];
          pp.transferRxCurrent = (peer.transferRx || 0) - pp.transferRxPrevious;
          pp.transferRxPrevious = peer.transferRx || 0;
          pp.transferTxCurrent = (peer.transferTx || 0) - pp.transferTxPrevious;
          pp.transferTxPrevious = peer.transferTx || 0;

          peer.transferTxCurrent = pp.transferTxCurrent;
          peer.transferRxCurrent = pp.transferRxCurrent;
          peer.hoverTx = pp.hoverTx;
          peer.hoverRx = pp.hoverRx;

          return peer;
        });
        if (seq !== this.allPeerRefreshSeq || remoteKey !== (this.activeRemoteId || 'local')) return;
        this.allPeers = all;
        this.allPeersLoaded = true;
      } catch (err) {
        if (seq !== this.allPeerRefreshSeq || remoteKey !== (this.activeRemoteId || 'local')) return;
        this.allPeersError = 'Failed to load peers.';
        console.error('refreshAllPeers failed:', err);
      } finally {
        if (seq === this.allPeerRefreshSeq && remoteKey === (this.activeRemoteId || 'local')) {
          this.allPeersLoading = false;
        }
      }
    },
};
