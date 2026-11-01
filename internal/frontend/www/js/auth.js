/**
 * Auth feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const authMethods = {
    blurAuthField() {
      this._authFocusSequence = (this._authFocusSequence || 0) + 1;
      const input = document.activeElement;
      if (input && ['login-username', 'login-password', 'totp-code'].includes(input.id)) input.blur();
    },
    focusLoginField() {
      const step = this.totpRequired;
      const sequence = this._authFocusSequence = (this._authFocusSequence || 0) + 1;
      this.$nextTick(() => {
        if (this.authenticated !== false || this.totpRequired !== step || sequence !== this._authFocusSequence) return;
        const input = this.totpRequired ? this.$refs.loginTOTP : this.$refs.loginUsername;
        if (input) input.focus({ preventScroll: true });
      });
    },
    // Keep feedback visible for at least one second while the request runs.
async _loginRequestWithDelay(request) {
      const [result] = await Promise.allSettled([
        Promise.resolve().then(request),
        new Promise(resolve => setTimeout(resolve, 1000)),
      ]);
      if (result.status === 'rejected') throw result.reason;
      return result.value;
    },

async login() {
      if (this.authenticating) return;
      const usernameInput = document.getElementById('login-username');
      const passwordInput = document.getElementById('login-password');
      if (usernameInput) this.username = usernameInput.value.trim();
      if (passwordInput) this.password = passwordInput.value;
      if (!this.username || !this.password) return;

      this.authenticating = true;
      try {
        const res = await this._loginRequestWithDelay(() => this.api.createSession({
          username: this.username,
          password: this.password,
          remember: this.remember,
        }));
        if (res && res.totp_required) {
          this.blurAuthField();
          this.lastAutoTOTP = '';
          this.totpRequired = true;
          this.totpCode = '';
          return;
        }
        await this._onLoginSuccess();
      } catch (err) {
        this.showToast(err.message || err.toString(), 'error');
      } finally {
        this.authenticating = false;
        this.password = '';
      }
    },

    backToLogin() {
      if (this.authenticating) return;
      this.blurAuthField();
      this.totpCode = '';
      this.lastAutoTOTP = '';
      this.totpRequired = false;
    },

    onTOTPInput(event) {
      this.totpCode = event.target.value;
      if (!/^\d{6}$/.test(this.totpCode)) {
        this.lastAutoTOTP = '';
        return;
      }
      if (!this.totpRequired || this.authenticating || this.lastAutoTOTP === this.totpCode) return;
      this.lastAutoTOTP = this.totpCode;
      this.loginStep2();
    },

    // Step 2: submit TOTP code after password was accepted.

async loginStep2() {
      if (!/^\d{6}$/.test(this.totpCode) || this.authenticating) return;
      this.authenticating = true;
      try {
        await this._loginRequestWithDelay(() => this.api.verifyTOTP({ code: this.totpCode }));
        await this._onLoginSuccess();
      } catch (err) {
        this.showToast(err.message || err.toString(), 'error');
      } finally {
        this.authenticating = false;
      }
    },

    // Called after a successful full authentication (steps 1 or 2).

async _onLoginSuccess() {
      const session = await this.api.getSession();
      if (!session.authenticated) throw new Error('Unable to confirm the authenticated session. Please try again.');
      this.blurAuthField();
      this.authenticated = true;
      this.totpRequired = false;
      this.totpCode = '';
      this.lastAutoTOTP = '';
      this.requiresPassword = session.requiresPassword;
      await this.refresh();
      // Re-load data that may have got 401 before login.
      this.loadTunnelInterfaces().then(() => {
        if (!this.activeInterfaceId) this.refreshAllPeers();
      }).catch(console.error);
      this.loadSettings();
      this.loadClientGroups();
      this.loadUsers();
      this.loadCurrentUser();
      this.loadRemotes();
      // Initialize the dashboard now; authenticated-only startup was skipped
      // while the login form was visible.
      this.loadDashboard();
      this.metricsStartPoller();
      if (this.activePage === 'gateways') {
        this.loadGateways();
        this.loadGatewayGroups();
        this.loadSystemInterfaces();
      }
      if (this.activePage === 'settings') {
        this.loadUsers();
      }
    },

logout(e) {
      e.preventDefault();

      this.api.deleteSession()
        .then(() => {
          this.authenticated = false;
      this._stopAliasJobPoll?.();
      this.clientsPersist = {};
      this.peersPersist = {};
      this.clientRefreshSeq = (this.clientRefreshSeq || 0) + 1;
          this.clients = null;
        })
        .catch((err) => {
          this.showToast(err.message || err.toString(), 'error');
        });
    },
};

