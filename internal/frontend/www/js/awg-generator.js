/** Version-aware Architect profile generation for the Vue 2 application. */
// Identity and target compatibility are retained when restoring recommendations.
const retainedFields = new Set(['protocolVersion', 'clientId', 'clientRelease', 'saveName']);

export const generatorMethods = {
  async openGenerateModal() {
    try {
      this.generatorCatalog = await this.api.getGeneratorOptions();
      this.generateForm = { ...this.generatorCatalog.defaults['3.1'], saveName: '' };
      this.generatorVersionChanged('3.1');
      this.generatedParams = null;
      this.generatedResult = null;
      this.generatedSignature = '';
      this.savingGeneratedTemplate = false;
      this.showGenerateModal = true;
    } catch (err) {
      this.showToast(`Unable to load generator: ${err.message}`, 'error');
    }
  },
  generatorSignature() {
    const { saveName, ...options } = this.generateForm;
    return JSON.stringify(options);
  },
  generatorPreset(version) {
    const defaults = this.generatorCatalog?.defaults[version];
    if (!defaults) return null;
    const preset = Object.fromEntries(Object.entries(defaults).filter(([key]) => !retainedFields.has(key)));
    const limits = this.generatorClient().limits;
    if (!limits.supportsI1I5) {
      for (const key of ['useTagC', 'useTagT', 'useTagR', 'useTagRC', 'useTagRD']) preset[key] = false;
    } else {
      preset.useTagC = preset.useTagC && limits.supportsCpsTagC && this.generatorCatalog.serverSupportsTagC;
      preset.useTagRC = preset.useTagRC && limits.supportsCpsTagRC;
      preset.useTagRD = preset.useTagRD && limits.supportsCpsTagRD;
    }
    return preset;
  },
  syncGeneratorRecommendation() {
    const preset = this.generatorPreset(this.generateForm.protocolVersion);
    if (preset && Object.keys(preset).some(key => this.generateForm[key] !== preset[key])) {
      this.generatorUseRecommended = false;
    }
  },
  applyGeneratorRecommendations() {
    if (!this.generatorUseRecommended) return;
    this.generatorVersionChanged(this.generateForm.protocolVersion);
  },
  invalidateGeneratedProfile() {
    if (this.generatedSignature && this.generatedSignature !== this.generatorSignature()) {
      this.generatedParams = null;
      this.generatedResult = null;
      this.generatedSignature = '';
    }
  },
  generatorVersionChanged(version) {
    const preset = this.generatorPreset(version);
    if (!preset) return;
    for (const [key, value] of Object.entries(preset)) this.generateForm[key] = value;
    this.generatorUseRecommended = true;
    this.invalidateGeneratedProfile();
  },
  generatorClient() {
    const client = this.generatorCatalog?.clients?.find(c => c.id === this.generateForm.clientId);
    if (!client) return { limits: {}, releases: [], notes: [] };
    const release = (client.releases || []).find(r => r.id === this.generateForm.clientRelease);
    return { ...client, limits: { ...client.limits, ...release?.limits } };
  },
  async generateParams() {
    if (this.generatingParams || this.savingGeneratedTemplate) return;
    this.generatingParams = true;
    this.generatedParams = null;
    this.generatedResult = null;
    const signature = this.generatorSignature();
    try {
      const { saveName, ...options } = this.generateForm;
      const result = await this.api.generateTemplate(options);
      if (!this.showGenerateModal || signature !== this.generatorSignature()) return;
      this.generatedSignature = signature;
      this.generatedResult = result;
      this.generatedParams = result.params;
    } catch (err) {
      this.showToast(`Generate failed: ${err.message}`, 'error');
    } finally {
      this.generatingParams = false;
    }
  },
  generatedTemplate() {
    if (!this.generatedResult || this.generatedSignature !== this.generatorSignature()) return null;
    return {
      ...this.generatedResult.params,
      name: this.generateForm.saveName.trim(),
      isDefault: false,
      protocolVersion: this.generatedResult.protocolVersion,
      mtu: this.generatedResult.mtu,
      host: this.generatedResult.options.host,
    };
  },
  async saveGeneratedTemplate() {
    if (this.generatingParams || this.savingGeneratedTemplate) return;
    const template = this.generatedTemplate();
    if (!template || !template.name) return;
    this.savingGeneratedTemplate = true;
    try {
      await this.api.createTemplate(template);
      await this.loadSettings();
      this.showGenerateModal = false;
      this.showToast(`Profile "${template.name}" saved`, 'success');
    } catch (err) {
      this.showToast(`Save failed: ${err.message}`, 'error');
    } finally {
      this.savingGeneratedTemplate = false;
    }
  },
  useGeneratedInForm() {
    if (this.generatingParams || this.savingGeneratedTemplate) return;
    const template = this.generatedTemplate();
    if (!template) return;
    this.templateForm = { ...template };
    this.templateEditTarget = null;
    this.showGenerateModal = false;
    this.showTemplateModal = true;
  },
};

// Shared by the application and the reactive behavior tests.
export const generatorWatchers = {
  generateForm: {
    deep: true,
    handler() {
      this.invalidateGeneratedProfile();
      this.syncGeneratorRecommendation();
    },
  },
  'generateForm.protocolVersion'(version) {
    this.generatorVersionChanged(version);
  },
};
