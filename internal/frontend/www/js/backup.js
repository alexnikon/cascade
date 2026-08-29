/**
 * Backup feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const backupMethods = {
openBackupModal() {
      this.backupPassword = '';
      this.backupPasswordConfirm = '';
      this.showBackupModal = true;
    },

async confirmDownloadBackup() {
      if (this.backupPassword && this.backupPassword !== this.backupPasswordConfirm) {
        this.showToast('Passwords do not match', 'error');
        return;
      }
      try {
        this.backupDownloading = true;
        const { blob, filename } = await this.api.downloadSystemBackup({ password: this.backupPassword, includeMetrics: this.backupIncludeMetrics });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
        this.showBackupModal = false;
        this.showToast('Backup created' + (this.backupPassword ? ' (encrypted)' : ''), 'success');
      } catch (err) {
        this.showToast(err.message || 'Backup failed', 'error');
      } finally {
        this.backupDownloading = false;
      }
    },

pickRestoreFile(file) {
      if (!file) return;
      const isEnc = file.name.endsWith('.enc');
      if (isEnc) {
        this.restoreFile = file;
        this.restorePassword = '';
        this.showRestorePasswordModal = true;
      } else {
        this._startRestorePreview(file, '');
      }
    },

cancelRestoreFlow() {
      this.showRestorePasswordModal = false;
      this.showRestorePreviewModal = false;
      this.restoreFile = null;
      this.restorePassword = '';
      this.restorePreview = null;
      this.restoreIfaceMap = {};
    },

async confirmRestoreWithPassword() {
      if (!this.restorePassword || this.restorePreviewLoading) return;
      const previewReady = await this._startRestorePreview(this.restoreFile, this.restorePassword);
      if (previewReady) this.showRestorePasswordModal = false;
    },

    // Step 1: preview — upload file, get interface mapping info.

async _startRestorePreview(file, password) {
      this.restorePreviewLoading = true;
      try {
        const preview = await this.api.previewSystemRestore({ file, password });
        this.restoreFile = file;
        this.restorePreview = preview;
        // Init ifaceMap: for each backup iface not found on server, default to first server iface.
        const map = {};
        for (const bi of (preview.backupIfaces || [])) {
          const found = (preview.serverIfaces || []).includes(bi);
          map[bi] = found ? bi : ((preview.serverIfaces || [])[0] || bi);
        }
        this.restoreIfaceMap = map;
        this.showRestorePreviewModal = true;
        return true;
      } catch (err) {
        this.showToast(err.message || 'Preview failed', 'error');
        return false;
      } finally {
        this.restorePreviewLoading = false;
      }
    },

    // Step 2: confirm — apply backup with optional remap.

async confirmRestoreApply() {
      this.showRestorePreviewModal = false;
      const file = this.restoreFile;
      const password = this.restorePassword || '';
      // Only send ifaceMap if remapping is actually needed.
      const ifaceMap = (this.restorePreview && this.restorePreview.needsRemap) ? this.restoreIfaceMap : null;
      await this._doRestore(file, password, ifaceMap);
      this.restoreFile = null;
      this.restorePassword = '';
      this.restorePreview = null;
      this.restoreIfaceMap = {};
    },

async _doRestore(file, password, ifaceMap) {
      try {
        this.systemRestoring = true;
        await this.api.restoreSystemBackup({ file, password, ifaceMap });
        this.showToast('Backup restored. Server is restarting…', 'success');
        // Poll until server is back online (up to 60s).
        const start = Date.now();
        const tryReload = () => {
          if (Date.now() - start > 60000) { window.location.reload(); return; }
          fetch(window.location.pathname).then(r => { if (r.ok) window.location.reload(); else setTimeout(tryReload, 2000); }).catch(() => setTimeout(tryReload, 2000));
        };
        setTimeout(tryReload, 3000);
      } catch (err) {
        this.showToast(err.message || 'Restore failed', 'error');
      } finally {
        this.systemRestoring = false;
      }
    },

async loadPreRestoreBackups() {
      this.preRestoreBackupsLoading = true;
      try {
        const res = await this.api.listSystemBackups();
        this.preRestoreBackups = res.backups || [];
      } catch (err) {
        this.preRestoreBackups = [];
      } finally {
        this.preRestoreBackupsLoading = false;
      }
    },

    // ========================================================================
    // Import Client Configs — restore private keys for peers imported without
    // a server-side key (e.g. peers migrated from another server's backup).
    // ========================================================================

openImportClientConfigs() {
      this.$refs.importClientConfigsInput.click();
    },

async onImportClientConfigsSelected(event) {
      const files = Array.from(event.target.files || []);
      event.target.value = '';
      if (files.length === 0) return;
      const interfaceId = this.currentInterface && this.currentInterface.id;
      if (!interfaceId) return;

      this.importClientConfigsLoading = true;
      try {
        const res = await this.api.importClientConfigs({ interfaceId, files });
        this.importClientConfigsResult = res;
        this.showImportClientConfigsResult = true;
        await this._refreshPeersOrAll();
        this.showToast(`${res.matched} config${res.matched === 1 ? '' : 's'} matched`, res.matched > 0 ? 'success' : 'error');
      } catch (err) {
        this.showToast(err.message || 'Import failed', 'error');
      } finally {
        this.importClientConfigsLoading = false;
      }
    },

    // ========================================================================
    // Firewall Rules Methods (replaces the former PBR / Policy section)
    // ========================================================================
};
