'use strict';

const messages = { // eslint-disable-line no-unused-vars
  en: {
    webAccess: {"title": "Web access", "description": "Restrict the panel to one administrative VPN. Other VPN clients cannot open it.", "unsupported": "Requires the updated Caddy configuration, Linux host networking, and a backend bound to localhost.", "adminOnly": "Manage this policy locally as an administrator through the administrative VPN.", "enabled": "Panel only through VPN", "interface": "Administrative VPN interface", "selectInterface": "Select an interface", "clients": "Trusted API clients", "clientsHelp": "Add each managing Cascade server and wgbot host separately. An exact source IP, its incoming interface, and a valid API token are required. These exceptions never open the panel.", "clientName": "Name", "clientIP": "Source IP", "ingress": "Incoming interface", "remove": "Remove", "addClient": "Add API client", "certificateHelp": "Keep the current HTTPS address and route its public IP through this VPN. The certificate, decoy site, and bot public pages remain available.", "apply": "Try for 120 seconds", "pending": "Connect through the selected VPN and confirm before this time. Otherwise the previous policy is restored automatically.", "confirm": "Confirm through VPN", "cancel": "Restore previous policy", "refresh": "Refresh access status", "trialStarted": "Trial started. Confirm through the administrative VPN.", "saved": "Web access policy updated."},
    name: 'Name',
    password: 'Password',
    signIn: 'Sign In',
    logout: 'Logout',
    updateAvailable: 'There is an update available!',
    update: 'Update',
    clients: 'Clients',
    new: 'New',
    deleteClient: 'Delete Client',
    deleteDialog1: 'Are you sure you want to delete',
    deleteDialog2: 'This action cannot be undone.',
    cancel: 'Cancel',
    create: 'Create',
    createdOn: 'Created on ',
    lastSeen: 'Last seen on ',
    totalDownload: 'Total Download: ',
    totalUpload: 'Total Upload: ',
    newClient: 'New Client',
    disableClient: 'Disable Client',
    enableClient: 'Enable Client',
    noClients: 'There are no clients yet.',
    noPrivKey: 'This client has no known private key. Cannot create Configuration.',
    downloadConfig: 'Download Configuration',
    madeBy: 'Made by',
    donate: 'Donate',
    theme: { dark: 'Dark theme', light: 'Light theme', auto: 'Auto theme' },
    restore: 'Restore',
    backup: 'Backup',
    titleRestoreConfig: 'Restore your configuration',
    titleBackupConfig: 'Backup your configuration',
    rememberMe: 'Remember me',
    titleRememberMe: 'Stay logged after closing the browser',
    sort: 'Sort',
    ExpireDate: 'Expire Date',
    Permanent: 'Permanent',
    // WAN Tunnels
    wanTunnels: 'WAN Tunnels',
    createTunnel: 'Create Tunnel',
    noWanTunnels: 'No WAN tunnels configured',
    tunnelName: 'Tunnel Name',
    protocol: 'Protocol',
    localSubnet: 'Local Subnet',
    remoteSubnet: 'Remote Subnet',
    remoteEndpoint: 'Remote Endpoint',
    remotePublicKey: 'Remote Public Key',
  },

};

// Existing locale preferences continue to resolve to the English dictionary.
for (const locale of ['ua', 'ru', 'tr', 'no', 'pl', 'fr', 'de', 'ca', 'es', 'ko', 'vi', 'nl', 'is', 'pt', 'it', 'th', 'hi']) {
  messages[locale] = messages.en;
}

export const i18n = new VueI18n({
  locale: localStorage.getItem('lang') || 'en',
  fallbackLocale: 'en',
  messages,
});
