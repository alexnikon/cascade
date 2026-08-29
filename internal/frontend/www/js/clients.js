/**
 * Clients feature methods for the Vue 2 application.
 * Methods intentionally operate on the Vue instance passed as this.
 */
import { sortByProperty } from './utils.js';

export const clientMethods = {
async refresh() {
      if (!this.authenticated) return;

      const clients = await this.api.getClients();
      this.clients = clients.map((client) => {
        if (client.name.includes('@') && client.name.includes('.') && this.avatarSettings.gravatar) {
          client.avatar = `https://gravatar.com/avatar/${sha256(client.name.toLowerCase().trim())}.jpg`;
        } else if (this.avatarSettings.dicebear) {
          client.avatar = `https://api.dicebear.com/9.x/${this.avatarSettings.dicebear}/svg?seed=${sha256(client.name.toLowerCase().trim())}`
        }

        if (!this.clientsPersist[client.id]) {
          this.clientsPersist[client.id] = {};
          this.clientsPersist[client.id].transferRxPrevious = client.transferRx;
          this.clientsPersist[client.id].transferTxPrevious = client.transferTx;
        }

        this.clientsPersist[client.id].transferRxCurrent = client.transferRx - this.clientsPersist[client.id].transferRxPrevious;
        this.clientsPersist[client.id].transferRxPrevious = client.transferRx;
        this.clientsPersist[client.id].transferTxCurrent = client.transferTx - this.clientsPersist[client.id].transferTxPrevious;
        this.clientsPersist[client.id].transferTxPrevious = client.transferTx;

        client.transferTxCurrent = this.clientsPersist[client.id].transferTxCurrent;
        client.transferRxCurrent = this.clientsPersist[client.id].transferRxCurrent;

        client.hoverTx = this.clientsPersist[client.id].hoverTx;
        client.hoverRx = this.clientsPersist[client.id].hoverRx;

        return client;
      });

      if (this.enableSortClient) {
        this.clients = sortByProperty(this.clients, 'name', this.sortClient);
      }
    },

createClient() {
      const name = this.clientCreateName;
      const expiredDate = this.clientExpiredDate;
      if (!name) return;

      this.api.createClient({ name, expiredDate })
        .catch((err) => this.showToast(err.message || err.toString(), 'error'))
        .finally(() => this.refresh().catch(console.error));
    },

deleteClient(client) {
      this.api.deleteClient({ clientId: client.id })
        .catch((err) => this.showToast(err.message || err.toString(), 'error'))
        .finally(() => this.refresh().catch(console.error));
    },

showOneTimeLink(client) {
      this.api.showOneTimeLink({ clientId: client.id })
        .catch((err) => this.showToast(err.message || err.toString(), 'error'))
        .finally(() => this.refresh().catch(console.error));
    },

enableClient(client) {
      this.api.enableClient({ clientId: client.id })
        .catch((err) => this.showToast(err.message || err.toString(), 'error'))
        .finally(() => this.refresh().catch(console.error));
    },

disableClient(client) {
      this.api.disableClient({ clientId: client.id })
        .catch((err) => this.showToast(err.message || err.toString(), 'error'))
        .finally(() => this.refresh().catch(console.error));
    },

updateClientName(client, name) {
      this.api.updateClientName({ clientId: client.id, name })
        .catch((err) => this.showToast(err.message || err.toString(), 'error'))
        .finally(() => this.refresh().catch(console.error));
    },

updateClientAddress(client, address) {
      this.api.updateClientAddress({ clientId: client.id, address })
        .catch((err) => this.showToast(err.message || err.toString(), 'error'))
        .finally(() => this.refresh().catch(console.error));
    },

updateClientExpireDate(client, expireDate) {
      this.api.updateClientExpireDate({ clientId: client.id, expireDate })
        .catch((err) => this.showToast(err.message || err.toString(), 'error'))
        .finally(() => this.refresh().catch(console.error));
    },

restoreConfig(e) {
      e.preventDefault();
      const file = e.currentTarget.files.item(0);
      if (file) {
        file.text()
          .then((content) => {
            this.api.restoreConfiguration(content)
              .then((_result) => this.showToast('The configuration was updated.'))
              .catch((err) => this.showToast(err.message || err.toString(), 'error'))
              .finally(() => this.refresh().catch(console.error));
          })
          .catch((err) => this.showToast(err.message || err.toString(), 'error'));
      } else {
        this.showToast('Failed to load your file!', 'error');
      }
    },
};
