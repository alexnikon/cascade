/**
 * Settings feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
import { i18n } from './i18n.js';

export const settingsMethods = {
async backupInterface() {
      if (!this.activeInterfaceId) return;
      try {
        const data = await this.api.backupTunnelInterface({ interfaceId: this.activeInterfaceId });
        const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
        const url = window.URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `${this.activeInterfaceId}.json`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        window.URL.revokeObjectURL(url);
      } catch (err) {
        this.showToast(`Backup failed: ${err.message}`, 'error');
      }
    },

restoreInterface(e) {
      if (!this.activeInterfaceId) return;
      const fileInput = e.currentTarget.files.item(0);
      if (!fileInput) return;
      fileInput.text()
        .then((content) => {
          const file = JSON.parse(content);
          return this.api.restoreTunnelInterface({ interfaceId: this.activeInterfaceId, file });
        })
        .then(() => {
          this.showToast('Configuration restored!');
          this.refreshPeers();
          this.loadTunnelInterfaces();
        })
        .catch((err) => this.showToast(`Restore failed: ${err.message}`, 'error'));
    },

    // ============================================================
    // Interconnect Export / Import
    // ============================================================

    /**
     * Export THIS interface's parameters as JSON for the remote side to import.
     *
     * Workflow: this side clicks "Export My Params" → sends file to remote side →
     * remote side clicks "Import JSON" → peer for us is created automatically.
     *
	 * File contains endpoint metadata and versioned AmneziaWG settings when applicable.
     * Remote side derives AllowedIPs subnet from our address (10.x.x.1/24 → 10.x.x.0/24).
     */

async exportMyInterfaceParams(iface) {
      try {
        const params = await this.api.exportInterfaceParams({ interfaceId: iface.id });
        const blob = new Blob([JSON.stringify(params, null, 2)], { type: 'application/json' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `${iface.id}-params.json`;
        a.click();
        URL.revokeObjectURL(url);
      } catch (err) {
        this.showToast(`Failed to export interface params: ${err.message}`, 'error');
      }
    },

    /**
     * Import remote side's interface params → create an interconnect peer for them.
     *
     * Workflow: remote side sends their export file → we click "Import JSON" →
     * peer is created automatically with their publicKey, endpoint, and derived AllowedIPs.
     */

importInterconnectPeerJSON() {
      if (!this.activeInterfaceId) return;
      const input = document.createElement('input');
      input.type = 'file';
      input.accept = '.json,application/json';
      input.onchange = async (e) => {
        const file = e.target.files[0];
        if (!file) return;
        try {
          const text = await file.text();
          const data = JSON.parse(text);
          const res = await this.api.importPeerJSON({ interfaceId: this.activeInterfaceId, ...data });
          await this.refreshPeers();
          await this.loadTunnelInterfaces();

          // A PSK from the file is already coordinated. A generated PSK must
          // be shared with the other side through Export My Params.
          const pskWasInFile = !!data.presharedKey;
          if (pskWasInFile) {
            this.showToast('Peer imported! PSK taken from the file — both sides are in sync.');
          } else {
            this.showToast('Peer imported! PSK generated — export your params and send to the remote side.', 'success', 10000);
          }
        } catch (err) {
          this.showToast(`Failed to import peer: ${err.message}`, 'error');
        }
      };
      input.click();
    },

async toggleDisableRoutes(iface) {
      try {
        await this.api.updateTunnelInterface({
          interfaceId: iface.id,
          disableRoutes: !iface.disableRoutes,
        });
        await this.loadTunnelInterfaces();
      } catch (err) {
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

    // ============================================================
    // Settings & Templates
    // ============================================================

async loadSettings() {
      try {
        this.globalSettings = await this.api.getSettings();
        if (this.globalSettings.lang && i18n.availableLocales.includes(this.globalSettings.lang)) {
          i18n.locale = this.globalSettings.lang;
          localStorage.setItem('lang', this.globalSettings.lang);
        }
        // Capture local server name once — never overwrite with remote settings.
        if (!this.activeRemoteId) {
          this.localServerName = this.globalSettings.routerName || this.globalSettings.hostname || '';
        }
        const { templates } = await this.api.getTemplates();
        this.templates = templates;
      } catch (err) {
        console.error('loadSettings failed:', err);
      }
    },

async loadMetricsSettings() {
      try {
        const settings = await this.api.getMetricsSettings();
        this.metricsSettings = { ...settings, token: '', clearToken: false };
      } catch (err) {
        console.error('loadMetricsSettings failed:', err);
      }
    },

async saveMetricsSettings() {
      if (!this.metricsSettings.canManage) return;
      try {
        const payload = {
          enabled: this.metricsSettings.enabled,
          port: Number(this.metricsSettings.port),
          connectedPeerThresholdSeconds: Number(this.metricsSettings.connectedPeerThresholdSeconds),
          historyEnabled: this.metricsSettings.historyEnabled,
          clearToken: this.metricsSettings.clearToken,
        };
        if (this.metricsSettings.token) payload.token = this.metricsSettings.token;
        const updated = await this.api.updateMetricsSettings(payload);
        this.metricsSettings = { ...updated, token: '', clearToken: false };
        this.metricsSettingsSaved = true;
        setTimeout(() => { this.metricsSettingsSaved = false; }, 2500);
      } catch (err) {
        this.showToast(`Failed to save metrics settings: ${err.message}`, 'error');
      }
    },

removeMetricsToken() {
      if (!this.metricsSettings.canManage) return;
      this.metricsSettings.token = '';
      this.metricsSettings.clearToken = true;
    },

metricsEndpointURL() {
      return `http://${window.location.hostname}:${this.metricsSettings.port || 9351}/metrics`;
    },

async saveSettings() {
      try {
        // Strip runtime-only fields and fields managed by dedicated save handlers.
        // defaultFwPolicy has its own saveDefaultFwPolicy() path — exclude here to
        // avoid triggering an unnecessary firewall RebuildChains on every Settings save.
		const {
		  hostname, resolvedPublicIP, publicIPWarning, defaultFwPolicy,
		  awgEngineVersion, awgToolsVersion, awgMaxProtocol, awg3Supported, awg3SupportError,
		  ...storable
		} = this.globalSettings;
        const updated = await this.api.updateSettings(storable);
        // Merge response back (includes fresh resolvedPublicIP / hostname).
        this.globalSettings = { ...this.globalSettings, ...updated };
        // Apply language change immediately.
        if (updated.lang && i18n.availableLocales.includes(updated.lang)) {
          i18n.locale = updated.lang;
          localStorage.setItem('lang', updated.lang);
        }
        this.settingsSaved = true;
        setTimeout(() => { this.settingsSaved = false; }, 2500);
      } catch (err) {
        this.showToast(`Failed to save settings: ${err.message}`, 'error');
      }
    },

async saveDefaultFwPolicy() {
      const policy = this.globalSettings.defaultFwPolicy;
      // Warn before applying DROP — remote sessions may lose connectivity if no
      // matching ACCEPT rule exists for management traffic (SSH, WireGuard, etc.).
      if (policy === 'drop') {
        const ok = window.confirm(
          'Set default policy to DROP?\n\n' +
          'All forward traffic not matched by an explicit rule above will be silently discarded.\n\n' +
          'Make sure you have ACCEPT rules covering your management traffic (SSH, WireGuard peers, etc.) ' +
          'before applying — otherwise you may lose remote access.'
        );
        if (!ok) {
          // Revert the select back to accept without saving
          await this.loadSettings();
          return;
        }
      }
      try {
        const updated = await this.api.updateSettings({ defaultFwPolicy: policy });
        this.globalSettings = { ...this.globalSettings, ...updated };
        this.showToast(`Default policy set to ${policy.toUpperCase()}`, 'success');
      } catch (err) {
        this.showToast(`Failed to update policy: ${err.message}`, 'error');
        // Revert optimistic change
        await this.loadSettings();
      }
    },

    // H1-H4: non-overlapping random ranges in the uint32 space.
    // 4 equal zones, each gets a ~50 M wide sub-range.

randomiseTemplateH() {
      const RANGE_SIZE = 50_000_000;
      const ZONE_SIZE = Math.floor((0xFFFFFFFF - 5) / 4);
      const r = (zone) => {
        const zs = 5 + zone * ZONE_SIZE;
        const ze = zs + ZONE_SIZE - 1;
        const start = zs + Math.floor(Math.random() * (ze - zs - RANGE_SIZE));
        return `${start}-${start + RANGE_SIZE}`;
      };
      this.templateForm.h1 = r(0);
      this.templateForm.h2 = r(1);
      this.templateForm.h3 = r(2);
      this.templateForm.h4 = r(3);
    },

openTemplateCreate() {
      this.templateForm = {
        name: '',
        isDefault: false,
		protocolVersion: '3.1',
        jc: 6, jmin: 10, jmax: 50,
		s1: 64, s2: 67, s3: 64, s4: 12,
        h1: '', h2: '', h3: '', h4: '',
        i1: '', i2: '', i3: '', i4: '', i5: '',
		headerProtectionKey: this.generateHeaderProtectionKey(),
		contentPaddingAddition: '10-100', rekeyAfterTime: '100-120', rekeyTimeout: '3-7',
		rejectAfterTime: '150-180', keepaliveTimeout: '5-15', maxHandshakeAttempts: '15-20',
		randomTrailers: true, disableCookies: true,
      };
      this.randomiseTemplateH();
      this.templateEditTarget = null;
      this.showTemplateModal = true;
    },

openTemplateEdit(tmpl) {
      this.templateForm = { ...tmpl };
      this.templateEditTarget = tmpl;
      this.showTemplateModal = true;
    },

	generateHeaderProtectionKey() {
	  const bytes = new Uint8Array(32);
	  crypto.getRandomValues(bytes);
	  return btoa(String.fromCharCode(...bytes));
	},

async saveTemplate() {
      try {
        if (this.templateEditTarget) {
          await this.api.updateTemplate({ templateId: this.templateEditTarget.id, ...this.templateForm });
        } else {
          await this.api.createTemplate(this.templateForm);
        }
        this.showTemplateModal = false;
        await this.loadSettings();
      } catch (err) {
        this.showToast(`Failed to save template: ${err.message}`, 'error');
      }
    },

async setDefaultTemplate(templateId) {
      try {
        await this.api.setDefaultTemplate({ templateId });
        await this.loadSettings();
      } catch (err) {
        this.showToast(`Failed: ${err.message}`, 'error');
      }
    },

async deleteTemplate(templateId) {
      if (!confirm('Delete this template?')) return;
      try {
        await this.api.deleteTemplate({ templateId });
        await this.loadSettings();
      } catch (err) {
        this.showToast(`Failed to delete template: ${err.message}`, 'error');
      }
    },

	/** Export a portable versioned AmneziaWG template as JSON. */

exportTemplateJSON(tmpl) {
      const params = {
        name: tmpl.name,
		protocolVersion: tmpl.protocolVersion || '2.0',
        jc: tmpl.jc, jmin: tmpl.jmin, jmax: tmpl.jmax,
        s1: tmpl.s1, s2: tmpl.s2, s3: tmpl.s3, s4: tmpl.s4,
        h1: tmpl.h1, h2: tmpl.h2, h3: tmpl.h3, h4: tmpl.h4,
        i1: tmpl.i1 || null, i2: tmpl.i2 || null, i3: tmpl.i3 || null,
        i4: tmpl.i4 || null, i5: tmpl.i5 || null,
		headerProtectionKey: tmpl.headerProtectionKey || undefined,
		contentPaddingAddition: tmpl.contentPaddingAddition || undefined,
		rekeyAfterTime: tmpl.rekeyAfterTime || undefined,
		rekeyTimeout: tmpl.rekeyTimeout || undefined,
		rejectAfterTime: tmpl.rejectAfterTime || undefined,
		keepaliveTimeout: tmpl.keepaliveTimeout || undefined,
		maxHandshakeAttempts: tmpl.maxHandshakeAttempts || undefined,
		randomTrailers: tmpl.randomTrailers,
		disableCookies: tmpl.disableCookies,
      };
      const blob = new Blob([JSON.stringify(params, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
	  a.download = `awg${(tmpl.protocolVersion || '2.0').replace('.', '')}-profile-${tmpl.name.replace(/[^a-zA-Z0-9_-]/g, '-')}.json`;
      a.click();
      URL.revokeObjectURL(url);
    },

	/** Import a portable AWG 2.0 or AWG 3.1 template JSON file. */

importTemplateJSON() {
      const input = document.createElement('input');
      input.type = 'file';
      input.accept = '.json,application/json';
      input.onchange = async (e) => {
        const file = e.target.files[0];
        if (!file) return;
        try {
          const text = await file.text();
          const data = JSON.parse(text);
		  // Ask for a name when the exported file did not contain one.
          if (!data.name) {
            const name = prompt('Enter a name for this profile:', file.name.replace(/\.json$/i, ''));
            if (!name) return;
            data.name = name;
          }
          await this.api.createTemplate(data);
          await this.loadSettings();
        } catch (err) {
          this.showToast(`Failed to import profile: ${err.message}`, 'error');
        }
      };
      input.click();
    },

	// ── Generate AmneziaWG modal ───────────────────────────────────────────────

openGenerateModal() {
	  this.generateForm = { profile: 'random', intensity: 'medium', host: '', browser: '', saveName: '', protocolVersion: '3.1' };
      this.generatedParams = null;
      this.savingGeneratedTemplate = false;
      this.showGenerateModal = true;
    },

async generateParams() {
      this.generatingParams = true;
      try {
        const res = await this.api.generateTemplate({
          profile:   this.generateForm.profile,
          intensity: this.generateForm.intensity,
          host:      this.generateForm.host || undefined,
          browser:   this.generateForm.browser || undefined,
		  protocolVersion: this.generateForm.protocolVersion,
        });
        this.generatedParams = res.params;
        if (!this.generateProfiles.length && res.profiles) {
          this.generateProfiles = res.profiles;
        }
      } catch (err) {
        this.showToast(`Generate failed: ${err.message}`, 'error');
      } finally {
        this.generatingParams = false;
      }
    },

async saveGeneratedTemplate() {
      if (!this.generatedParams || this.savingGeneratedTemplate) return;
      const name = this.generateForm.saveName.trim();
      if (!name) {
        this.showToast('Enter a template name before saving', 'error');
        return;
      }
      this.savingGeneratedTemplate = true;
      try {
		await this.api.createTemplate({ name, protocolVersion: this.generateForm.protocolVersion, ...this.generatedParams });
        await this.loadSettings();
        this.showGenerateModal = false;
        this.showToast(`Profile "${name}" saved`, 'success');
      } catch (err) {
        this.showToast(`Save failed: ${err.message}`, 'error');
      } finally {
        this.savingGeneratedTemplate = false;
      }
    },

async copyPublicIP() {
      const ip = (this.globalSettings.resolvedPublicIP || '').trim();
      if (!ip) return;
      try {
        await navigator.clipboard.writeText(ip);
        this.showToast('Public IP copied to clipboard', 'success');
      } catch (_) {
        this.showToast('Failed to copy public IP', 'error');
      }
    },

useGeneratedInForm() {
      if (!this.generatedParams) return;
      const p = this.generatedParams;
      this.templateForm = {
        name: this.generateForm.saveName || '',
        isDefault: false,
		protocolVersion: this.generateForm.protocolVersion,
        host: this.generateForm.host || '',
        jc: p.jc, jmin: p.jmin, jmax: p.jmax,
        s1: p.s1, s2: p.s2, s3: p.s3, s4: p.s4,
        h1: p.h1, h2: p.h2, h3: p.h3, h4: p.h4,
        i1: p.i1 || '', i2: p.i2 || '', i3: p.i3 || '',
        i4: p.i4 || '', i5: p.i5 || '',
		headerProtectionKey: p.headerProtectionKey || '',
		contentPaddingAddition: p.contentPaddingAddition || '',
		rekeyAfterTime: p.rekeyAfterTime || '', rekeyTimeout: p.rekeyTimeout || '',
		rejectAfterTime: p.rejectAfterTime || '', keepaliveTimeout: p.keepaliveTimeout || '',
		maxHandshakeAttempts: p.maxHandshakeAttempts || '',
		randomTrailers: p.randomTrailers, disableCookies: p.disableCookies,
      };
      this.templateEditTarget = null;
      this.showGenerateModal = false;
      this.showTemplateModal = true;
    },

	/** Fill the interface form from a protocol-compatible template. */

onInterfaceTemplateSelect(templateId) {
	  if (!templateId) return;
      const tmpl = (this.templates || []).find(t => t.id === templateId);
      if (!tmpl) return;
	  const version = this.interfaceCreate.protocol === 'amneziawg-3.1' ? '3.1' : '2.0';
	  if ((tmpl.protocolVersion || '2.0') !== version) {
		this.showToast(`This template is for AWG ${tmpl.protocolVersion || '2.0'}`, 'error');
		return;
	  }
      this.interfaceCreate.settings = {
        jc: tmpl.jc,    jmin: tmpl.jmin,  jmax: tmpl.jmax,
        s1: tmpl.s1,    s2: tmpl.s2,      s3: tmpl.s3,   s4: tmpl.s4,
        h1: tmpl.h1,    h2: tmpl.h2,      h3: tmpl.h3,   h4: tmpl.h4,
        i1: tmpl.i1 || '', i2: tmpl.i2 || '', i3: tmpl.i3 || '',
        i4: tmpl.i4 || '', i5: tmpl.i5 || '',
		headerProtectionKey: tmpl.headerProtectionKey || '',
		contentPaddingAddition: tmpl.contentPaddingAddition || '',
		rekeyAfterTime: tmpl.rekeyAfterTime || '', rekeyTimeout: tmpl.rekeyTimeout || '',
		rejectAfterTime: tmpl.rejectAfterTime || '', keepaliveTimeout: tmpl.keepaliveTimeout || '',
		maxHandshakeAttempts: tmpl.maxHandshakeAttempts || '',
		randomTrailers: tmpl.randomTrailers, disableCookies: tmpl.disableCookies,
      };
    },

    // ========================================================================
    // Users Management
    // ========================================================================

async loadUsers() {
      try {
        const res = await this.api.getUsers();
        this.users = res.users || [];
      } catch (err) {
        console.error('loadUsers failed:', err);
        this.users = [];
      }
    },

async loadCurrentUser() {
      try {
        this.currentUser = await this.api.getCurrentUser();
      } catch (err) {
        this.currentUser = null;
      }
    },

async createFirstUser() {
      const { username, password, passwordConfirm } = this.firstRunForm;
      if (!username) { this.showToast('Username is required', 'error'); return; }
      if (!password) { this.showToast('Password is required', 'error'); return; }
      if (password.length < 8) { this.showToast('Password must be at least 8 characters', 'error'); return; }
      if (password !== passwordConfirm) { this.showToast('Passwords do not match', 'error'); return; }
      this.firstRunSaving = true;
      try {
        await this.api.createUser({ username, password });
        // Open mode ended — reload to show login screen with real authentication.
        this.firstRunSaving = false;
        window.location.reload();
      } catch (err) {
        this.showToast(err.message || 'Failed to create admin account', 'error');
        this.firstRunSaving = false;
      }
    },

async createUser() {
      const { username, password, passwordConfirm } = this.addUserForm;
      if (!username) { this.showToast('Username is required', 'error'); return; }
      if (!password) { this.showToast('Password is required', 'error'); return; }
      if (password.length < 8) { this.showToast('Password must be at least 8 characters', 'error'); return; }
      if (password !== passwordConfirm) { this.showToast('Passwords do not match', 'error'); return; }
      try {
        await this.api.createUser({ username, password });
        this.showAddUserModal = false;
        this.addUserForm = { username: '', password: '', passwordConfirm: '' };
        await this.loadUsers();
        this.showToast(`User "${username}" created`);
      } catch (err) {
        this.showToast(err.message || 'Failed to create user', 'error');
      }
    },

async deleteUser(user) {
      if (!confirm(`Delete user "${user.username}"?`)) return;
      try {
        await this.api.deleteUser(user.id);
        await this.loadUsers();
        this.showToast(`User "${user.username}" deleted`);
      } catch (err) {
        this.showToast(err.message || 'Failed to delete user', 'error');
      }
    },

async setUserAdmin(user) {
      const granting = !user.is_admin;
      const action = granting ? `grant admin to "${user.username}"` : `remove admin from "${user.username}"`;
      if (!confirm(`Are you sure you want to ${action}?`)) return;
      try {
        const res = await this.api.setUserAdmin(user.id, granting);
        if (res && res.user) {
          const idx = this.users.findIndex(u => u.id === res.user.id);
          if (idx !== -1) this.users.splice(idx, 1, res.user);
          if (this.currentUser && res.user.id === this.currentUser.id) {
            this.currentUser = res.user;
          }
        }
        this.showToast(granting ? `Admin granted to "${user.username}"` : `Admin removed from "${user.username}"`);
      } catch (err) {
        this.showToast(err.message || 'Failed to update admin role', 'error');
      }
    },

    // ========================================================================
    // API Tokens
    // ========================================================================

async loadApiTokens() {
      try {
        const res = await this.api.getApiTokens();
        this.apiTokens = res.tokens || [];
      } catch (err) {
        this.apiTokens = [];
      }
    },

async createApiToken() {
      const { name } = this.createTokenForm;
      if (!name) { this.showToast('Token name is required', 'error'); return; }
      try {
        const res = await this.api.createApiToken({ name });
        this.showCreateTokenModal = false;
        this.createTokenForm = { name: '' };
        this.newTokenValue = res.raw_token || '';
        this.showNewTokenModal = true;
        await this.loadApiTokens();
      } catch (err) {
        this.showToast(err.message || 'Failed to create token', 'error');
      }
    },

async deleteApiToken(token) {
      if (!confirm(`Revoke token "${token.name}"? This cannot be undone.`)) return;
      try {
        await this.api.deleteApiToken({ id: token.id });
        await this.loadApiTokens();
        this.showToast(`Token "${token.name}" revoked`);
      } catch (err) {
        this.showToast(err.message || 'Failed to revoke token', 'error');
      }
    },

copyTokenToClipboard() {
      if (!this.newTokenValue) return;
      navigator.clipboard.writeText(this.newTokenValue)
        .then(() => this.showToast('Token copied to clipboard'))
        .catch(() => this.showToast('Failed to copy — select and copy manually', 'error'));
    },

    // ========================================================================
    // TOTP Setup
    // ========================================================================

async openTOTPSetup() {
      try {
        const res = await this.api.getTOTPSetup();
        this.totpSetupSecret = res.secret || '';
        this.totpSetupQrPng  = res.qr_png  || '';
        this.totpSetupQrUri  = res.qr_uri  || '';
        this.totpSetupCode   = '';
        this.totpSetupSaving = false;
        this.showTOTPSetupModal = true;
      } catch (err) {
        this.showToast(err.message || 'Failed to start TOTP setup', 'error');
      }
    },

async confirmTOTPEnable() {
      if (!this.totpSetupCode) { this.showToast('Enter the 6-digit code', 'error'); return; }
      this.totpSetupSaving = true;
      try {
        const res = await this.api.enableTOTP({ code: this.totpSetupCode });
        this.showTOTPSetupModal = false;
        this.totpSetupCode = '';
        // Update the user list and current user to reflect totp_enabled=true.
        if (res && res.user) {
          this.currentUser = res.user;
          const idx = this.users.findIndex(u => u.id === res.user.id);
          if (idx !== -1) this.users.splice(idx, 1, res.user);
        }
        this.showToast('Two-factor authentication enabled');
      } catch (err) {
        this.showToast(err.message || 'Failed to enable 2FA', 'error');
      } finally {
        this.totpSetupSaving = false;
      }
    },

openTOTPDisable() {
      this.totpDisableCode = '';
      this.showTOTPDisableModal = true;
    },

async confirmTOTPDisable() {
      if (!this.totpDisableCode) { this.showToast('Enter the 6-digit code', 'error'); return; }
      try {
        const res = await this.api.disableTOTP({ code: this.totpDisableCode });
        this.showTOTPDisableModal = false;
        this.totpDisableCode = '';
        if (res && res.user) {
          this.currentUser = res.user;
          const idx = this.users.findIndex(u => u.id === res.user.id);
          if (idx !== -1) this.users.splice(idx, 1, res.user);
        }
        this.showToast('Two-factor authentication disabled');
      } catch (err) {
        this.showToast(err.message || 'Failed to disable 2FA', 'error');
      }
    },
    // ========================================================================
    // Wizard: Simple Client VPN
    // ========================================================================

    // Returns a random low port (400–999) not used by any existing interface.
    // 443 is excluded — Caddy binds UDP 443 for QUIC/HTTP3 in the standard deploy.
};
