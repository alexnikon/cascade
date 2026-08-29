/**
 * Nat feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const natMethods = {
switchNatTab(tab) {
      this.activeNatTab = tab;
      if (tab === 'portforward' && this.dnatRules.length === 0) {
        this.loadDnatRules();
      }
    },

    // ── Port Forwarding (DNAT) ────────────────────────────────────────────────

async loadDnatRules() {
      this.dnatLoading = true;
      try {
        const res = await this.api.call({ method: 'GET', path: '/nat/dnat' });
        this.dnatRules = res.rules || [];
      } catch (e) {
        this.showToast(e.message || 'Failed to load port forwarding rules', 'error');
      } finally {
        this.dnatLoading = false;
      }
    },

    // portInPool returns true if port is within any range/entry in the portPool string.
    // portPool format: "51831-65535" | "433-442,8080,51831-65535"
    // Returns true (no warning) when portPool is empty/unset.

portInPool(port) {
      const pool = (this.globalSettings && this.globalSettings.portPool) || '';
      if (!pool.trim()) return true;
      const p = parseInt(port);
      if (!p || p < 1 || p > 65535) return true; // let backend validate the value itself
      for (const part of pool.split(',')) {
        const s = part.trim();
        const dash = s.indexOf('-');
        if (dash > 0) {
          const lo = parseInt(s.slice(0, dash));
          const hi = parseInt(s.slice(dash + 1));
          if (p >= lo && p <= hi) return true;
        } else {
          if (parseInt(s) === p) return true;
        }
      }
      return false;
    },

openAddDnat() {
      this.dnatEditMode = false;
      this.dnatForm = { id: '', name: '', protocol: 'udp', inInterface: '', inPort: '', dest: '', destPort: '', masquerade: true, comment: '' };
      this.showDnatModal = true;
    },

openEditDnat(rule) {
      this.dnatEditMode = true;
      this.dnatForm = {
        id: rule.id,
        name: rule.name,
        protocol: rule.protocol,
        inInterface: rule.inInterface || '',
        inPort: rule.inPort,
        dest: rule.dest || rule.destIP,  // fallback to destIP for old rules migrated without dest
        destPort: rule.destPort || '',
        masquerade: rule.masquerade !== false, // default true for old rules without the field
        comment: rule.comment || '',
      };
      this.showDnatModal = true;
    },

async saveDnat() {
      const body = {
        name: this.dnatForm.name,
        protocol: this.dnatForm.protocol,
        inInterface: this.dnatForm.inInterface || '',
        inPort: parseInt(this.dnatForm.inPort) || 0,
        dest: this.dnatForm.dest,
        destPort: parseInt(this.dnatForm.destPort) || 0,
        masquerade: !!this.dnatForm.masquerade,
        comment: this.dnatForm.comment,
      };
      try {
        if (this.dnatEditMode) {
          await this.api.call({ method: 'PATCH', path: `/nat/dnat/${this.dnatForm.id}`, body });
          this.showToast('Rule updated', 'success');
        } else {
          await this.api.call({ method: 'POST', path: '/nat/dnat', body });
          this.showToast('Rule created', 'success');
        }
        this.showDnatModal = false;
        await this.loadDnatRules();
      } catch (e) {
        this.showToast(e.message || 'Failed to save rule', 'error');
      }
    },

async toggleDnat(rule) {
      try {
        await this.api.call({ method: 'PATCH', path: `/nat/dnat/${rule.id}`, body: { enabled: !rule.enabled } });
        await this.loadDnatRules();
      } catch (e) {
        this.showToast(e.message || 'Failed to toggle rule', 'error');
      }
    },

async deleteDnat(rule) {
      if (!confirm(`Delete "${rule.name}"?`)) return;
      try {
        await this.api.call({ method: 'DELETE', path: `/nat/dnat/${rule.id}` });
        this.showToast('Rule deleted', 'success');
        await this.loadDnatRules();
      } catch (e) {
        this.showToast(e.message || 'Failed to delete rule', 'error');
      }
    },

    // Navigate to the interface page for an auto NAT rule

goToInterface(interfaceId) {
      this.activePage = 'interfaces';
      this.activeInterfaceId = interfaceId;
    },

async loadNatInterfaces() {
      try {
        const res = await this.api.getNatInterfaces();
        this.natInterfaces = res.interfaces || [];
        // Select the first available interface when none is selected.
        if (!this.natRuleCreate.outInterface && this.natInterfaces.length > 0) {
          this.natRuleCreate.outInterface = this.natInterfaces[0].name;
        }
      } catch (err) {
        console.error('loadNatInterfaces error:', err);
        this.natInterfaces = [];
      }
    },

async loadNatRules() {
      this.natRulesLoading = true;
      try {
        const res = await this.api.getNatRules();
        this.natRules = res.rules || [];
      } catch (err) {
        console.error('loadNatRules error:', err);
        this.natRules = [];
      } finally {
        this.natRulesLoading = false;
      }
    },

    /**
     * Open the NAT rule edit modal.
     * Convert rule.source into sourceType and sourceValue for the UI.
     */

openNatRuleEdit(rule) {
      let sourceType = 'any';
      let sourceValue = '';
      let sourceAliasId = '';
      if (rule.sourceAliasId) {
        sourceType = 'alias';
        sourceAliasId = rule.sourceAliasId;
      } else if (rule.source) {
        sourceType = rule.source.includes('/') ? 'subnet' : 'ip';
        sourceValue = rule.source;
      }
      this.natRuleEdit = {
        id:            rule.id,
        name:          rule.name,
        sourceType,
        sourceValue,
        sourceAliasId,
        outInterface:  rule.outInterface,
        type:          rule.type,
        toSource:      rule.toSource || '',
        comment:       rule.comment || '',
      };
      this.showNatRuleEdit = true;
    },

    /**
     * Calculate the final source value from the form fields.
     * sourceType='any'    → '' (no -s argument in iptables)
     * sourceType='alias'  → '' (source is empty, sourceAliasId is set)
     * sourceType='subnet' → sourceValue (CIDR)
     * sourceType='ip'     → sourceValue (single IP)
     */

_natFormSource(form) {
      if (form.sourceType === 'any' || form.sourceType === 'alias') return '';
      return (form.sourceValue || '').trim();
    },

    /** Aliases usable as L3 NAT sources (host/network/group/ipset, excluding port types). */

_natIpAliases() {
      return (this.aliases || []).filter(a =>
        ['host', 'network', 'group', 'ipset'].includes(a.type)
      );
    },

    /**
     * Display source text for the rules table.
     */

_natRuleSourceLabel(rule) {
      if (!rule.source) return 'any';
      return rule.source;
    },

    /**
     * Display type text for the rules table.
     */

_natRuleTypeLabel(rule) {
      if (rule.type === 'MASQUERADE') return 'MASQUERADE';
      return `SNAT → ${rule.toSource}`;
    },

async createNatRule() {
      try {
        const data = {
          name:          this.natRuleCreate.name,
          source:        this._natFormSource(this.natRuleCreate),
          sourceAliasId: this.natRuleCreate.sourceType === 'alias' ? this.natRuleCreate.sourceAliasId : null,
          outInterface:  this.natRuleCreate.outInterface,
          type:          this.natRuleCreate.type,
          toSource:      this.natRuleCreate.type === 'SNAT' ? this.natRuleCreate.toSource : null,
          comment:       this.natRuleCreate.comment,
        };
        await this.api.createNatRule(data);
        // Reset the form.
        this.showNatRuleCreate = false;
        this.natRuleCreate = {
          name: '', sourceType: 'any', sourceValue: '', sourceAliasId: '',
          outInterface: this.natInterfaces.length > 0 ? this.natInterfaces[0].name : '',
          type: 'MASQUERADE', toSource: '', comment: '',
        };
        await this.loadNatRules();
        this.showToast('NAT rule created', 'success');
      } catch (err) {
        this.showToast(err.message || 'Failed to create NAT rule', 'error');
      }
    },

async saveNatRule() {
      try {
        const data = {
          name:          this.natRuleEdit.name,
          source:        this._natFormSource(this.natRuleEdit),
          sourceAliasId: this.natRuleEdit.sourceType === 'alias' ? this.natRuleEdit.sourceAliasId : null,
          outInterface:  this.natRuleEdit.outInterface,
          type:          this.natRuleEdit.type,
          toSource:      this.natRuleEdit.type === 'SNAT' ? this.natRuleEdit.toSource : null,
          comment:       this.natRuleEdit.comment,
        };
        await this.api.updateNatRule({ ruleId: this.natRuleEdit.id, ...data });
        this.showNatRuleEdit = false;
        await this.loadNatRules();
        this.showToast('NAT rule updated', 'success');
      } catch (err) {
        this.showToast(err.message || 'Failed to update NAT rule', 'error');
      }
    },

async toggleNatRule(rule) {
      try {
        await this.api.toggleNatRule({ ruleId: rule.id, enabled: !rule.enabled });
        await this.loadNatRules();
      } catch (err) {
        this.showToast(err.message || 'Failed to toggle NAT rule', 'error');
      }
    },

async deleteNatRule(rule) {
      if (!confirm(`Delete NAT rule "${rule.name}"?`)) return;
      try {
        await this.api.deleteNatRule({ ruleId: rule.id });
        await this.loadNatRules();
        this.showToast('NAT rule deleted', 'success');
      } catch (err) {
        this.showToast(err.message || 'Failed to delete NAT rule', 'error');
      }
    },

    // ========================================================================
    // Firewall Aliases Methods
    // ========================================================================
};
