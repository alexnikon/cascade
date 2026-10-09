/**
 * Aliases feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const aliasMethods = {
async loadClientGroups() {
      try {
        const res = await this.api.getClientGroups();
        this.clientGroups = res.groups || [];
      } catch (err) {
        console.error('loadClientGroups error:', err);
        this.clientGroups = [];
      }
    },

defaultGroupId() {
      const g = this.clientGroups.find(g => g.name === 'default');
      return g ? g.id : '';
    },

    // Creates a new client-group inline from the peer create modals.
    // targetModel: 'manual' | 'quick'

async createInlineClientGroup(targetModal) {
      const name = this.inlineGroupInput.trim();
      if (!name) return;

      // Basic validation: letters, digits, hyphens, underscores; must start with letter
      if (!/^[a-zA-Z][a-zA-Z0-9_-]{0,62}$/.test(name)) {
        this.showToast(this.$t('toast.aliasInvalidName'), 'error');
        return;
      }

      // Check if already exists
      const existing = this.clientGroups.find(g => g.name.toLowerCase() === name.toLowerCase());
      if (existing) {
        this.showToast(this.$t('toast.aliasGroupExists', { name }), 'error');
        if (targetModal === 'manual') {
          this.peerCreate.groupId = existing.id;
          this.inlineGroupShow = false;
        } else {
          this.peerCreateGroupId = existing.id;
          this.inlineGroupShowQuick = false;
        }
        this.inlineGroupInput = '';
        return;
      }

      try {
        const res = await this.api.createClientGroup({ name });
        await this.loadClientGroups();
        const created = this.clientGroups.find(g => g.id === res.id || g.name === name);
        if (created) {
          if (targetModal === 'manual') {
            this.peerCreate.groupId = created.id;
            this.inlineGroupShow = false;
          } else {
            this.peerCreateGroupId = created.id;
            this.inlineGroupShowQuick = false;
          }
        }
        this.inlineGroupInput = '';
        this.showToast(this.$t('toast.aliasGroupCreated', { name }));
      } catch (err) {
        this.showToast(this.$t('toast.aliasGroupCreateFailed', { error: err.message }), 'error');
      }
    },

_resetAliasCreate() {
      this.aliasCreate = {
        name: '', description: '', type: 'network', entries: '', ipsetEntries: '', memberIds: [],
        genSource: 'country', genCountry: '', genAsn: '', genAsnList: '', genGranularity: 16,
        file: null, rateDown: 0, rateUp: 0,
      };
    },

    // Return only host/network aliases (candidates for group membership).

_aliasGroupCandidates() {
      return this.aliases.filter(a => a.type === 'host' || a.type === 'network');
    },

    // Return only port aliases (candidates for port-group membership).

_portAliasCandidates() {
      return this.aliases.filter(a => a.type === 'port');
    },

    // Return port and port-group aliases for firewall rule port selectors.

_portAliasOptions() {
      return this.aliases.filter(a => a.type === 'port' || a.type === 'port-group');
    },

    // ── Domain aliases ────────────────────────────────────────────────────────
    // Runtime state (counts, timestamps, resolver errors) lives in memory on the
    // server and arrives on the alias object as `domainStatus`. It is never
    // persisted, so it can be absent right after creation.

_domainStatusLabel(alias) {
      const st = alias && alias.domainStatus;
      if (!st) return { text: this.$t('alias.statusStarting'), tone: 'muted' };
      if (st.suffixCount && !Object.values(st.dnsProxy || {}).some(s => s.ready)) return { text: this.$t('alias.statusNeedsDns'), tone: 'muted' };
      if (st.suffixCount && !st.exactCount && !st.ipv4Count && !st.ipv6Count) return { text: this.$t('alias.statusWaitingDns'), tone: 'muted' };
      if (st.lastError) return { text: this.$t('alias.statusDnsError'), tone: 'error' };
      if (!st.lastUpdate) return { text: this.$t('alias.statusResolving'), tone: 'muted' };
      return { text: this.$t('alias.statusHealthy'), tone: 'ok' };
    },

    // Local-time formatting for lastUpdate / nextUpdate; '—' when unset.

_domainTime(value) {
      if (!value) return '—';
      const d = new Date(value);
      return isNaN(d.getTime()) ? '—' : d.toLocaleTimeString();
    },

toggleDomainDetails(alias) {
      this.aliasDomainOpenId = this.aliasDomainOpenId === alias.id ? '' : alias.id;
    },

    // Manual refresh. The endpoint returns 204 as soon as the resolver is woken,
    // so poll the alias list briefly to pick up the new counts.

async refreshDomainAlias(alias) {
      if (this.aliasRefreshingId) return;
      this.aliasRefreshingId = alias.id;
      try {
        await this.api.refreshAlias({ id: alias.id });
        const before = alias.domainStatus ? alias.domainStatus.lastUpdate : '';
        for (let i = 0; i < 10; i++) {
          await new Promise(r => setTimeout(r, 600));
          await this.loadAliases();
          const cur = this.aliases.find(a => a.id === alias.id);
          if (cur && cur.domainStatus && cur.domainStatus.lastUpdate !== before) break;
        }
        const cur = this.aliases.find(a => a.id === alias.id);
        if (cur && cur.domainStatus && cur.domainStatus.lastError) {
          this.showToast(this.$t('toast.aliasRefreshWithErrors', { error: cur.domainStatus.lastError }), 'error');
        } else {
          this.showToast(this.$t('toast.aliasDomainsReresolved'), 'success');
        }
      } catch (err) {
        this.showToast(err.message || this.$t('toast.aliasRefreshFailed'), 'error');
      } finally {
        this.aliasRefreshingId = '';
      }
    },

async createAlias() {
      try {
        const data = { name: this.aliasCreate.name, description: this.aliasCreate.description, type: this.aliasCreate.type };
        if (data.type === 'host' || data.type === 'network') {
          data.entries = this.aliasCreate.entries.split('\n').map(l => l.trim()).filter(Boolean);
        }
        if (data.type === 'port' || data.type === 'domain') {
          data.entries = this.aliasCreate.entries.split('\n').map(l => l.trim()).filter(Boolean);
        }
        if (data.type === 'group' || data.type === 'port-group') {
          data.memberIds = this.aliasCreate.memberIds;
        }
        if (data.type === 'client-group') {
          data.rateDown = this.aliasCreate.rateDown || 0;
          data.rateUp = this.aliasCreate.rateUp || 0;
        }
        // Preserve ipsetEntries, file, and genOpts before resetting the form.
        const ipsetText = this.aliasCreate.ipsetEntries.trim();
        const uploadFile = this.aliasCreate.file;
        const genOpts = this.aliasCreate.type === 'ipset' ? {
          source:      this.aliasCreate.genSource,
          country:     this.aliasCreate.genCountry,
          asn:         this.aliasCreate.genAsn,
          asnList:     this.aliasCreate.genAsnList,
          granularity: this.aliasCreate.genGranularity,
        } : null;

        const res = await this.api.createAlias(data);
        const created = res.alias || res; // The server returns { alias: {...} }.
        this.showAliasCreate = false;
        this._resetAliasCreate();
        await this.loadAliases();

        // For ipset, upload content with manual input taking precedence over a file.
        if (created.type === 'ipset' && ipsetText) {
          try {
            const result = await this.api.uploadAliasFile({ id: created.id, text: ipsetText });
            await this.loadAliases();
            const count = result && result.entryCount ? result.entryCount : '?';
            this.showToast(this.$t('toast.aliasCreatedEntriesLoaded', { count }), 'success');
          } catch (err) {
            this.showToast(this.$t('toast.aliasCreatedEntriesUploadFailed', { error: err.message }), 'error');
          }
        } else if (created.type === 'ipset' && uploadFile) {
          try {
            const text = await uploadFile.text();
            const result = await this.api.uploadAliasFile({ id: created.id, text });
            await this.loadAliases();
            const count = result && result.entryCount ? result.entryCount : '?';
            this.showToast(this.$t('toast.aliasCreatedEntriesUploaded', { count, file: uploadFile.name }), 'success');
          } catch (err) {
            this.showToast(this.$t('toast.aliasCreatedFileUploadFailed', { error: err.message }), 'error');
          }
        } else {
          if (created.type === 'client-group') await this.loadClientGroups();
          this.showToast(this.$t('toast.aliasCreated'), 'success');
        }

        // Auto-generate when the alias is an ipset with a configured source.
        if (created.type === 'ipset' && genOpts &&
            (genOpts.country || genOpts.asn || genOpts.asnList)) {
          await this._startAliasGenerate(created.id, genOpts);
        }
      } catch (err) {
        this.showToast(err.message || this.$t('toast.aliasCreateFailed'), 'error');
      }
    },

    // ── Country picker helpers ────────────────────────────────────────────────

openCountryPicker(form) {
      this.countryPickerForm = form;
      const code = form === 'create' ? this.aliasCreate.genCountry : this.aliasEdit.genCountry;
      const found = code ? this.countries.find(c => c.code === code.toUpperCase()) : null;
      this.countrySearch = found ? found.name + ' (' + found.code + ')' : '';
      this.showCountryDrop = true;
    },

selectCountry(c) {
      if (this.countryPickerForm === 'create') {
        this.aliasCreate.genCountry = c.code;
      } else {
        this.aliasEdit.genCountry = c.code;
      }
      this.countrySearch = c.name + ' (' + c.code + ')';
      this.showCountryDrop = false;
    },

onCountryBlur() {
      // Delay so mousedown on item fires first.
      setTimeout(() => { this.showCountryDrop = false; }, 150);
    },

async openAliasEdit(alias) {
      const hasEntries = alias.type === 'host' || alias.type === 'network' ||
                         alias.type === 'port' || alias.type === 'domain';
      const hasMembers = alias.type === 'group' || alias.type === 'port-group';
      this.aliasEdit = {
        id: alias.id,
        name: alias.name,
        description: alias.description || '',
        type: alias.type,
        entryCount: alias.entryCount || 0,
        generatorOpts: alias.generatorOpts || null,
        entries: hasEntries ? (alias.entries || []).join('\n') : '',
        ipsetEntries: '',
        ipsetEntriesLoading: false,
        ipsetEntriesLoaded: false,
        memberIds: hasMembers ? [...(alias.memberIds || [])] : [],
        genSource: alias.generatorOpts?.asnList ? 'asn-list' : alias.generatorOpts?.asn ? 'asn' : 'country',
        genCountry: alias.generatorOpts?.country || '',
        genAsn: alias.generatorOpts?.asn || '',
        genAsnList: alias.generatorOpts?.asnList || '',
        genGranularity: alias.generatorOpts?.clientGranularity ?? 16,
        rateDown: alias.rateDown || 0,
        rateUp: alias.rateUp || 0,
      };
      // Pre-fill country picker display if country was saved.
      const code = this.aliasEdit.genCountry;
      const found = code ? this.countries.find(c => c.code === code.toUpperCase()) : null;
      this.countrySearch = found ? found.name + ' (' + found.code + ')' : '';
      this.showAliasEdit = true;

      // For small manually-entered ipsets: pre-populate textarea from kernel.
      // Criteria: no generatorOpts (not generated) AND entryCount <= 200.
      if (alias.type === 'ipset' && !alias.generatorOpts && alias.entryCount <= 200) {
        this.aliasEdit.ipsetEntriesLoading = true;
        try {
          const res = await this.api.getAliasEntries({ id: alias.id });
          this.aliasEdit.ipsetEntries = (res.entries || []).join('\n');
          this.aliasEdit.ipsetEntriesLoaded = true;
        } catch (e) {
          this.aliasEdit.ipsetEntries = '';
        } finally {
          this.aliasEdit.ipsetEntriesLoading = false;
        }
      }
    },

async saveAliasEdit() {
      try {
        const data = { id: this.aliasEdit.id, name: this.aliasEdit.name, description: this.aliasEdit.description };
        if (this.aliasEdit.type === 'host' || this.aliasEdit.type === 'network') {
          data.entries = this.aliasEdit.entries.split('\n').map(l => l.trim()).filter(Boolean);
        }
        if (this.aliasEdit.type === 'port' || this.aliasEdit.type === 'domain') {
          data.entries = this.aliasEdit.entries.split('\n').map(l => l.trim()).filter(Boolean);
        }
        if (this.aliasEdit.type === 'group' || this.aliasEdit.type === 'port-group') {
          data.memberIds = this.aliasEdit.memberIds;
        }
        if (this.aliasEdit.type === 'client-group') {
          data.rateDown = this.aliasEdit.rateDown || 0;
          data.rateUp = this.aliasEdit.rateUp || 0;
        }
        await this.api.updateAlias(data);

        // For ipset: if user edited entries in textarea — upload new content
        const ipsetText = this.aliasEdit.ipsetEntries.trim();
        if (this.aliasEdit.type === 'ipset' && ipsetText) {
          try {
            const result = await this.api.uploadAliasFile({ id: this.aliasEdit.id, text: ipsetText });
            const count = result && result.entryCount ? result.entryCount : '?';
            this.showToast(this.$t('toast.aliasUpdatedEntriesSaved', { count }), 'success');
          } catch (err) {
            this.showToast(this.$t('toast.aliasSavedEntriesUploadFailed', { error: err.message }), 'error');
          }
        } else {
          this.showToast(this.$t('toast.aliasUpdated'), 'success');
        }

        this.showAliasEdit = false;
        await this.loadAliases();
        if (this.aliasEdit.type === 'client-group') await this.loadClientGroups();
      } catch (err) {
        this.showToast(err.message || this.$t('toast.aliasUpdateFailed'), 'error');
      }
    },

async deleteAlias(alias) {
      const msg = alias.type === 'client-group'
        ? this.$t('toast.aliasGroupDeleteConfirm', { name: alias.name })
        : this.$t('toast.aliasDeleteConfirm', { name: alias.name });
      if (!confirm(msg)) return;
      try {
        const res = await this.api.deleteAlias({ id: alias.id });
        await this.loadAliases();
        await this.loadClientGroups();
        if (alias.type === 'client-group') {
          const moved = res && res.movedCount ? res.movedCount : 0;
          const toast = moved > 0
            ? `Group "${alias.name}" deleted. ${moved} peer${moved > 1 ? 's' : ''} moved to default.`
            : this.$t('toast.aliasGroupDeleted', { name: alias.name });
          this.showToast(toast, 'success', 8000);
        } else {
          this.showToast(this.$t('toast.aliasDeleted'), 'success');
        }
      } catch (err) {
        this.showToast(err.message || this.$t('toast.aliasDeleteFailed'), 'error');
      }
    },

async uploadAliasFile(aliasId, file) {
      try {
        const text = await file.text();
        const result = await this.api.uploadAliasFile({ id: aliasId, text });
        await this.loadAliases();
        const count = result && result.entryCount ? result.entryCount : '?';
        this.showToast(this.$t('toast.aliasUploadedEntries', { count, file: file.name }), 'success');
      } catch (err) {
        this.showToast(err.message || this.$t('toast.aliasUploadFailed'), 'error');
      }
    },

async startAliasGenerate(alias) {
      await this._startAliasGenerate(alias.id, {
        source: this.aliasEdit.genSource,
        country: this.aliasEdit.genCountry,
        asn: this.aliasEdit.genAsn,
        asnList: this.aliasEdit.genAsnList,
        granularity: this.aliasEdit.genGranularity,
      });
    },

async startAliasGenerateCreate(aliasId) {
      await this._startAliasGenerate(aliasId, {
        source: this.aliasCreate.genSource,
        country: this.aliasCreate.genCountry,
        asn: this.aliasCreate.genAsn,
        asnList: this.aliasCreate.genAsnList,
      });
    },

// Re-run the stored generator for an alias sourced from RIPEstat, so its
    // prefix list can be refreshed straight from the aliases table.
    async refreshGeneratedAlias(alias) {
      const g = alias.generatorOpts || {};
      await this._startAliasGenerate(alias.id, {
        source:      g.asnList ? 'asn-list' : g.asn ? 'asn' : 'country',
        country:     g.country,
        asn:         g.asn,
        asnList:     g.asnList,
        granularity: g.clientGranularity || 0,
      });
    },

async _startAliasGenerate(aliasId, opts) {
      try {
        const genOpts = {};
        const src = opts?.source || 'country';
        if (src === 'country' && opts?.country) genOpts.country = opts.country;
        else if (src === 'asn' && opts?.asn) genOpts.asn = opts.asn;
        else if (src === 'asn-list' && opts?.asnList) genOpts.asnList = opts.asnList;
        else {
          this.showToast(this.$t('toast.aliasGenerateSourceRequired'), 'error');
          return;
        }
        // Rounding narrows the routes pushed to clients; the ipset keeps the
        // exact prefixes, so firewall rules using this alias stay precise.
        const granularity = Number(opts?.granularity) || 0;
        if (granularity > 0) genOpts.clientGranularity = granularity;
        const { jobId } = await this.api.generateAlias({ id: aliasId, ...genOpts });
        this.aliasGeneratingId = aliasId;
        this.aliasGenerateJobId = jobId;
        this.aliasGenerateJobStatus = { status: 'running' };
        this.showToast(this.$t('toast.aliasGenerationStarted'), 'success');
        this._pollAliasJob(aliasId, jobId);
      } catch (err) {
        this.showToast(err.message || this.$t('toast.aliasGenerationStartFailed'), 'error');
      }
    },

_pollAliasJob(aliasId, jobId) {
      const interval = setInterval(async () => {
        try {
          const status = await this.api.getAliasJobStatus({ id: aliasId, jobId });
          this.aliasGenerateJobStatus = status;
          if (status.status === 'done') {
            clearInterval(interval);
            this.aliasGeneratingId = null;
            this.aliasGenerateJobId = null;
            await this.loadAliases();
            this.showToast(this.$t('toast.aliasGenerationDone', { count: status.entryCount }), 'success');
          } else if (status.status === 'error') {
            clearInterval(interval);
            this.aliasGeneratingId = null;
            this.showToast(this.$t('toast.aliasGenerationFailed', { error: status.error }), 'error');
          }
        } catch (err) {
          clearInterval(interval);
          this.aliasGeneratingId = null;
          console.error('_pollAliasJob error:', err);
        }
      }, 3000);
    },

_aliasLabel(aliasId) {
      if (!aliasId || aliasId === 'any') return this.$t('fw.any');
      const a = this.aliases.find(x => x.id === aliasId);
      return a ? a.name : aliasId;
    },

async openAliasFromBadge(aliasId) {
      if (!aliasId) return;
      this.activePage = 'firewall-aliases';
      if (!this.aliases.length) await this.loadAliases();
      const alias = this.aliases.find(a => a.id === aliasId);
      if (alias) await this.openAliasEdit(alias);
    },

async showAliasTooltip(event, aliasId) {
      const alias = this.aliases.find(a => a.id === aliasId);
      if (!alias) return;
      const rect = event.target.getBoundingClientRect();
      this.aliasTooltip = { id: aliasId, alias, x: rect.left, y: rect.bottom + 4, ipsetEntries: null, ipsetLoading: false };

      if (alias.type === 'ipset' || alias.type === 'client-group') {
        this.aliasTooltip.ipsetLoading = true;
        try {
          const res = await this.api.getAliasEntries({ id: aliasId });
          // Only update if still hovering the same alias
          if (this.aliasTooltip && this.aliasTooltip.id === aliasId) {
            this.aliasTooltip = Object.assign({}, this.aliasTooltip, {
              ipsetEntries: res.entries || [],
              ipsetLoading: false,
            });
          }
        } catch (e) {
          if (this.aliasTooltip && this.aliasTooltip.id === aliasId) {
            this.aliasTooltip = Object.assign({}, this.aliasTooltip, { ipsetLoading: false });
          }
        }
      }
    },

hideAliasTooltip() {
      this.aliasTooltip = null;
    },

    // ========================================================================
    // System Backup / Restore

async loadAliases() {
      this.aliasesLoading = true;
      try {
        const res = await this.api.getAliases();
        this.aliases = Array.isArray(res) ? res : (res.aliases || []);
      } catch (err) {
        console.error('loadAliases error:', err);
        this.aliases = [];
      } finally {
        this.aliasesLoading = false;
      }
    },

    // ── Dashboard ──────────────────────────────────────────────────────────────
};
