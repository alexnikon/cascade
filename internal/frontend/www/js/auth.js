/**
 * Auth feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
export const authMethods = {
login() {
      const usernameInput = document.getElementById('login-username');
      const passwordInput = document.getElementById('login-password');
      if (usernameInput) this.username = usernameInput.value.trim();
      if (passwordInput) this.password = passwordInput.value;
      if (!this.username || !this.password) return;
      if (this.authenticating) return;

      this.authenticating = true;
      this.api.createSession({
        username: this.username,
        password: this.password,
        remember: this.remember,
      })
        .then(async (res) => {
          // Server may require TOTP as a second step.
          if (res && res.totp_required) {
            this.totpRequired = true;
            this.totpCode = '';
            return; // stay on login screen — show TOTP input
          }
          // Fully authenticated (no TOTP or TOTP already done).
          await this._onLoginSuccess();
        })
        .catch((err) => {
          this.showToast(err.message || err.toString(), 'error');
        })
        .finally(() => {
          this.authenticating = false;
          this.password = '';
        });
    },

    // Step 2: submit TOTP code after password was accepted.

async loginStep2() {
      if (!this.totpCode || this.authenticating) return;
      this.authenticating = true;
      try {
        await this.api.verifyTOTP({ code: this.totpCode });
        this.totpRequired = false;
        this.totpCode = '';
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
      this.authenticated = session.authenticated;
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
          this.clients = null;
        })
        .catch((err) => {
          this.showToast(err.message || err.toString(), 'error');
        });
    },
};

