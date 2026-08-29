/**
 * Firewall feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const firewallMethods = {
async loadFirewallRules() {
      this.firewallRulesLoading = true;
      try {
        const [rulesRes, pendingRes] = await Promise.all([
          this.api.getFirewallRules(),
          this.api.getFirewallPending(),
        ]);
        this.firewallRules = Array.isArray(rulesRes) ? rulesRes : (rulesRes.rules || []);
        this.firewallPending = pendingRes.hasPendingChanges || false;
      } catch (err) {
        console.error('loadFirewallRules error:', err);
        this.firewallRules = [];
      } finally {
        this.firewallRulesLoading = false;
      }
      // $nextTick must be AFTER finally so firewallRulesLoading=false is already
      // flushed to DOM before we look for tbody#fw-rules-tbody.
      this.$nextTick(() => this._initSortable());
    },

    // Initialise (or re-initialise) Sortable.js on the firewall rules tbody.
    // Called after every loadFirewallRules() via $nextTick so the DOM is up-to-date.

_initSortable() {
      if (typeof Sortable === 'undefined') return;
      const tbody = document.getElementById('fw-rules-tbody');
      if (!tbody) return;
      if (this._sortableInstance) {
        this._sortableInstance.destroy();
        this._sortableInstance = null;
      }
      this._sortableInstance = Sortable.create(tbody, {
        handle: '.drag-handle',
        draggable: 'tr[data-id]',
        animation: 150,
        ghostClass: 'fw-drag-ghost',
        forceFallback: true,
        fallbackTolerance: 0,
        onEnd: (evt) => {
          if (evt.oldIndex === evt.newIndex) return;
          // Debounce: cancel any in-flight reorder and wait 120ms for rapid consecutive drags.
          clearTimeout(this._reorderTimer);
          this._reorderTimer = setTimeout(() => {
            const ids = Array.from(tbody.querySelectorAll('tr[data-id]')).map(tr => tr.dataset.id);
            this._reorderFirewallRules(ids);
          }, 120);
        },
      });
    },

async _reorderFirewallRules(ids) {
      try {
        await this.api.reorderFirewallRules(ids);
        await this.loadFirewallRules();
      } catch (err) {
        this.showToast(err.message || 'Failed to reorder rules', 'error');
        await this.loadFirewallRules(); // revert DOM to server state
      }
    },

async applyFirewallRules() {
      this.firewallApplying = true;
      try {
        await this.api.applyFirewallRules();
        this.firewallPending = false;
        this.showToast('Firewall rules applied', 'success');
      } catch (err) {
        this.showToast(err.message || 'Failed to apply firewall rules', 'error');
      } finally {
        this.firewallApplying = false;
      }
    },

async discardFirewallChanges() {
      if (!confirm('Discard all unapplied changes and revert to last applied state?')) return;
      try {
        await this.api.discardFirewallChanges();
        await this.loadFirewallRules();
        this.showToast('Changes discarded', 'success');
      } catch (err) {
        this.showToast(err.message || 'Failed to discard changes', 'error');
      }
    },

async loadFirewallInterfaces() {
      try {
        const res = await this.api.getFirewallInterfaces();
        this.firewallInterfaces = Array.isArray(res) ? res : (res.interfaces || []);
      } catch (err) {
        this.firewallInterfaces = [];
      }
    },

_resetFirewallCreate() {
      this.firewallCreate = {
        name: '',
        interface: 'any',
        protocol: 'any',
        source:      { type: 'any', aliasId: '', value: '', invert: false, portMode: '', port: '', portAliasId: '' },
        destination: { type: 'any', aliasId: '', value: '', invert: false, portMode: '', port: '', portAliasId: '' },
        action: 'accept',
        gatewayId: '', gatewayGroupId: '', useGroup: false,
        fallbackToDefault: false,
        log: false, comment: '',
      };
    },

_buildFirewallPayload(form) {
      const buildEp = (ep) => {
        if (!ep || ep.type === 'any') {
          // Even 'any' endpoints may carry port info
          const portInfo = ep?.portMode === 'alias'
            ? { port: null,               portAliasId: ep.portAliasId || null }
            : { port: ep?.port || null,   portAliasId: null };
          return { type: 'any', invert: false, ...portInfo };
        }
        const portInfo = ep.portMode === 'alias'
          ? { port: null,           portAliasId: ep.portAliasId || null }
          : { port: ep.port || null, portAliasId: null };
        const base = { type: ep.type, invert: Boolean(ep.invert), ...portInfo };
        if (ep.type === 'alias') return { ...base, aliasId: ep.aliasId };
        if (ep.type === 'cidr')  return { ...base, value: ep.value };
        return { type: 'any', invert: false, port: null, portAliasId: null };
      };
      return {
        name:           form.name,
        interface:      form.interface  || 'any',
        protocol:       form.protocol   || 'any',
        source:         buildEp(form.source),
        destination:    buildEp(form.destination),
        action:         form.action     || 'accept',
        gatewayId:         form.useGroup ? null : (form.gatewayId || null),
        gatewayGroupId:    form.useGroup ? (form.gatewayGroupId || null) : null,
        fallbackToDefault: Boolean(form.fallbackToDefault),
        log:               Boolean(form.log),
        comment:           form.comment || '',
      };
    },

openFirewallCreate() {
      this._resetFirewallCreate();
      this.showFirewallCreate = true;
    },

async createFirewallRule() {
      try {
        const payload = this._buildFirewallPayload(this.firewallCreate);
        await this.api.createFirewallRule(payload);
        this.showFirewallCreate = false;
        this._resetFirewallCreate();
        await this.loadFirewallRules();
        this.showToast('Firewall rule created', 'success');
      } catch (err) {
        this.showToast(err.message || 'Failed to create firewall rule', 'error');
      }
    },

openFirewallEdit(rule) {
      const loadEp = (ep) => {
        if (!ep) return { type: 'any', aliasId: '', value: '', invert: false, portMode: '', port: '', portAliasId: '' };
        const portMode     = ep.portAliasId ? 'alias' : (ep.port ? 'plain' : '');
        const portAliasId  = ep.portAliasId || '';
        const port         = ep.port        || '';
        return {
          type:        ep.type      || 'any',
          aliasId:     ep.aliasId   || '',
          value:       ep.value     || '',
          invert:      Boolean(ep.invert),
          portMode,
          port,
          portAliasId,
        };
      };
      this.firewallEdit = {
        id:          rule.id,
        name:        rule.name,
        interface:   rule.interface  || 'any',
        protocol:    rule.protocol   || 'any',
        source:      loadEp(rule.source),
        destination: loadEp(rule.destination),
        action:      rule.action || 'accept',
        gatewayId:      rule.gatewayId      || '',
        gatewayGroupId: rule.gatewayGroupId || '',
        useGroup:          !!rule.gatewayGroupId,
        fallbackToDefault: Boolean(rule.fallbackToDefault),
        log:               Boolean(rule.log),
        comment:           rule.comment || '',
      };
      this.showFirewallEdit = true;
    },

async saveFirewallEdit() {
      try {
        const payload = { id: this.firewallEdit.id, ...this._buildFirewallPayload(this.firewallEdit) };
        await this.api.updateFirewallRule(payload);
        this.showFirewallEdit = false;
        await this.loadFirewallRules();
        this.showToast('Firewall rule updated', 'success');
      } catch (err) {
        this.showToast(err.message || 'Failed to update firewall rule', 'error');
      }
    },

async toggleFirewallRule(rule) {
      try {
        await this.api.toggleFirewallRule({ id: rule.id, enabled: !rule.enabled });
        await this.loadFirewallRules();
      } catch (err) {
        this.showToast(err.message || 'Failed to toggle firewall rule', 'error');
      }
    },

async deleteFirewallRule(rule) {
      if (!confirm(`Delete firewall rule "${rule.name}"?`)) return;
      try {
        await this.api.deleteFirewallRule({ id: rule.id });
        await this.loadFirewallRules();
        this.showToast('Firewall rule deleted', 'success');
      } catch (err) {
        this.showToast(err.message || 'Failed to delete firewall rule', 'error');
      }
    },

async moveFirewallRule(rule, direction) {
      try {
        await this.api.moveFirewallRule({ id: rule.id, direction });
        await this.loadFirewallRules();
      } catch (err) {
        this.showToast(err.message || 'Failed to move firewall rule', 'error');
      }
    },

openAddSeparator() {
      this.separatorEdit = { name: '', color: '' };
      this.separatorEditId = null;
      this.showSeparatorModal = true;
    },

openEditSeparator(rule) {
      this.separatorEdit = { name: rule.name, color: rule.separatorColor || '' };
      this.separatorEditId = rule.id;
      this.showSeparatorModal = true;
    },

async saveSeparator() {
      const { name, color } = this.separatorEdit;
      try {
        if (this.separatorEditId) {
          await this.api.updateFirewallRule({ id: this.separatorEditId, ruleType: 'separator', name: name.trim() || 'Separator', color });
        } else {
          await this.api.createFirewallRule({ ruleType: 'separator', name: name.trim() || 'Separator', color });
        }
        this.showSeparatorModal = false;
        await this.loadFirewallRules();
        this.showToast(this.separatorEditId ? 'Separator updated' : 'Separator added', 'success');
      } catch (err) {
        this.showToast(err.message || 'Failed to save separator', 'error');
      }
    },

    // Returns inline style for a separator row based on its color value.

_sepRowStyle(color) {
      const map = {
        '':       { border: '#6b7280', bg: '#f3f4f6', darkBg: '#404040' },
        'red':    { border: '#ef4444', bg: '#fff0f0', darkBg: '#4a1a1a' },
        'orange': { border: '#f97316', bg: '#fff4eb', darkBg: '#4a2a10' },
        'yellow': { border: '#eab308', bg: '#fefce8', darkBg: '#3a3510' },
        'green':  { border: '#22c55e', bg: '#f0fdf4', darkBg: '#1a3d22' },
        'cyan':   { border: '#06b6d4', bg: '#ecfeff', darkBg: '#153040' },
        'blue':   { border: '#3b82f6', bg: '#eff6ff', darkBg: '#1a2a4a' },
        'purple': { border: '#a855f7', bg: '#faf5ff', darkBg: '#2e1a4a' },
      };
      const c = map[color] || map[''];
      const bg = this.theme === 'dark' ? c.darkBg : c.bg;
      return `background:${bg}; border-left:3px solid ${c.border};`;
    },

_firewallEndpointLabel(ep) {
      if (!ep || ep.type === 'any') return 'Any';
      const inv = ep.invert ? 'NOT ' : '';
      let label = '';
      if (ep.type === 'alias') label = inv + this._aliasLabel(ep.aliasId);
      else if (ep.type === 'cidr') label = inv + (ep.value || '');
      else label = 'Any';
      if (ep.port) label += ':' + ep.port;
      return label;
    },

_firewallGatewayLabel(rule) {
      if (rule.gatewayGroupId) {
        const g = (this.gatewayGroups || []).find(x => x.id === rule.gatewayGroupId);
        return g ? `Group: ${g.name}` : '—';
      }
      if (rule.gatewayId) {
        const g = (this.gateways || []).find(x => x.id === rule.gatewayId);
        return g ? g.name : '—';
      }
      return '—';
    },

_firewallActionStyle(action, enabled) {
      if (!enabled) {
        // Disabled rule — muted grey badge regardless of action.
        return 'background:#e5e7eb; color:#9ca3af;';
      }
      if (action === 'accept') return 'background:#dcfce7; color:#15803d;';
      if (action === 'drop')   return 'background:#fee2e2; color:#dc2626;';
      if (action === 'reject') return 'background:#ffedd5; color:#ea580c;';
      return '';
    },

    // ========================================================================
    // Quick peer create (one-click, admin-tunnel style)
    // ========================================================================
};

