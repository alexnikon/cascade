/* eslint-disable no-console */
/* eslint-disable no-undef */
/* eslint-disable no-new */

'use strict';

import { API } from './api.js';
import { i18n } from './i18n.js';
import { bytes, commonMethods } from './utils.js';
import { notificationMethods } from './notifications.js';
import { authMethods } from './auth.js';
import { clientMethods } from './clients.js';
import { navigationMethods } from './navigation.js';
import { interfaceMethods } from './interfaces.js';
import { gatewayMethods } from './gateways.js';
import { routingMethods } from './routing.js';
import { natMethods } from './nat.js';
import { dashboardMethods } from './dashboard.js';
import { diagnosticsMethods } from './diagnostics.js';
import { aliasMethods } from './aliases.js';
import { backupMethods } from './backup.js';
import { firewallMethods } from './firewall.js';
import { peerMethods } from './peers.js';
import { pollerMethods } from './poller.js';
import { settingsMethods } from './settings.js';
import { wizardMethods } from './wizards.js';
new Vue({
  el: '#app',
  components: {
    apexchart: VueApexCharts,
  },
  i18n,
  data: {
    authenticated: null,
    authenticating: false,
    versionInfo: null,       // populated by loadVersionInfo() — version + update status
    updateBannerDismissed: false, // hides update banner until next loadVersionInfo() call
    updateChecking: false,        // spinner state for "Check for updates" button
    username: '',          // login form username field
    password: null,
    requiresPassword: null,
    remember: false,
    rememberMeEnabled: false,

    // TOTP login step 2
    totpCode: '',          // 6-digit code input during login
    totpRequired: false,   // true = password OK, show TOTP input
    totpPending: false,    // waiting for TOTP verification

    // Users management
    users: [],
    currentUser: null,

    // TOTP setup modal (for /users/me/totp/setup flow)
    showTOTPSetupModal: false,
    totpSetupSecret: '',
    totpSetupQrPng: '',
    totpSetupQrUri: '',
    totpSetupCode: '',
    totpSetupSaving: false,

    // Disable TOTP modal
    showTOTPDisableModal: false,
    totpDisableCode: '',

    // First-run setup (open mode — no users yet)
    showFirstRunSetup: false,
    firstRunForm: { username: 'admin', password: '', passwordConfirm: '' },
    firstRunSaving: false,

    // Add user modal
    showAddUserModal: false,
    addUserForm: { username: '', password: '', passwordConfirm: '' },

    // API Tokens
    apiTokens: [],
    showCreateTokenModal: false,
    createTokenForm: { name: '' },
    showNewTokenModal: false,   // shown after successful creation
    newTokenValue: '',          // raw token — displayed once, never stored

    clients: null,
    clientsPersist: {},
    clientDelete: null,
    clientCreate: null,
    clientCreateName: '',
    clientExpiredDate: '',
    clientEditName: null,
    clientEditNameId: null,
    clientEditAddress: null,
    clientEditAddressId: null,
    clientEditExpireDate: null,
    clientEditExpireDateId: null,
    qrcode: null,

    uiTrafficStats: false,

    avatarSettings: {
      'dicebear': null,
      'gravatar': false,
    },
    enableOneTimeLinks: false,
    enableSortClient: false,
    sortClient: true, // Sort clients by name, true = asc, false = desc
    enableExpireTime: false,

    // Sidebar navigation
    activePage: 'dashboard', // 'dashboard' | 'interfaces' | 'gateways' | 'routing' | 'firewall' | 'settings' | 'administration'
    activeInterfaceId: null,  // ID of the selected interface tab
    hoverPage: null,          // hovered sidebar page
    mobileNavOpen: false,
    isCompactViewport: window.matchMedia('(max-width: 1023px)').matches,
    compactMediaQuery: null,
    sidebarMenu: [
      { id: 'dashboard',        label: 'Dashboard' },
      { id: 'interfaces',       label: 'Interfaces' },
      { id: 'gateways',         label: 'Gateways' },
      { id: 'routing',          label: 'Routing' },
      { id: 'nat',              label: 'NAT' },
      { id: '_header_firewall', label: 'Firewall', type: 'header' },
      { id: 'firewall-aliases', label: 'Aliases' },
      { id: 'firewall',         label: 'Rules' },
      { id: 'diagnostics',      label: 'Diagnostics' },
      { id: 'remotes',          label: 'Remotes' },
      { id: 'settings',         label: 'Settings' },
      { id: 'administration',   label: 'Administration' },
      { id: '_header_wizards',  label: 'Wizards', type: 'header' },
      { id: 'wizard-simple-vpn', label: 'Simple Client VPN' },
      { id: 'wizard-uplink-vpn', label: 'Cascade via WireGuard Uplink' },
      { id: 'wizard-cascade-s2s', label: 'Cascade ↔ Cascade S2S' },
    ],

    // ── Dashboard ──────────────────────────────────────────────────────────────
    dashWidgets: [],          // [{id, type, x, y, w, h}]
    dashGrid: null,           // GridStack instance
    dashShowAddMenu: false,
    dashSystemInfo: null,
    dashAvailableWidgetTypes: [
      { type: 'server-info',    label: 'Server Info',     icon: '🖥️' },
      { type: 'interfaces',     label: 'Interfaces',      icon: '🔌' },
      { type: 'gateways',       label: 'Gateways',        icon: '📡' },
      { type: 'peers-summary',  label: 'Peers Summary',   icon: '👥' },
      { type: 'peers',          label: 'Peers',           icon: '🔗' },
      { type: 'nat',            label: 'NAT',             icon: '🔀' },
      { type: 'traffic',        label: 'Traffic',         icon: '📊' },
      { type: 'monitoring',     label: 'Monitoring',      icon: '📈' },
    ],
    dashPeersState: {},   // per-widget: { [widgetId]: { iface: '', sort: 'name' } }

    // ── Monitoring widget ──────────────────────────────────────────────────────
    metricsSnapshot: null,          // latest GET /api/metrics response
    metricsAvailableKeys: [],       // all keys (populated on first snapshot)
    metricsHistory: {},             // { [widgetId+key]: [{x,y}] } rolling buffer
    metricsGatewayDist: {},         // { [widgetId+key]: [[ts_ms, healthy, degraded, down, adminDown]] }
    metricsGatewaySeriesCache: {},  // { [widgetId+key]: [series] } — stable refs so ApexCharts skips re-render
    metricsAreaSeriesCache: {},     // { [widgetId+key]: [{name, data:[]}] } — stable refs for area charts
    metricsPoller: null,            // setInterval handle
    metricsHistoryPromise: null,    // prevents overlapping history refreshes
    metricsTickPromise: null,       // prevents overlapping metrics refreshes
    systemInfoPromise: null,        // prevents overlapping system-info refreshes
    resourcePoller: null,           // shared UI resource polling handle
    resourcePollPromise: null,      // prevents overlapping scheduler ticks
    refreshPeersPromise: null,      // prevents overlapping interface peer requests
    refreshPeersPromiseKey: null,   // interface ID associated with the in-flight request
    refreshAllPeersPromise: null,   // prevents overlapping aggregate peer requests
    refreshAllPeersPromiseKey: null, // remote identity associated with the request
    resourcePollSkipped: 0,         // diagnostics counter for coalesced refreshes
    metricsConfigWidget: null,      // widget being configured (modal open)
    metricsConfigPage: 'dashboard', // page the config modal was opened from
    metricsConfigDraft: [],         // draft graphs[] for config modal
    metricsColorDraft: {},          // { [key]: '#rrggbb' } for config modal
    metricsTitleDraft: '',          // draft widget title for config modal
    metricsWidgetPeriod: {},        // { [widgetId]: '5m'|'1h'|... }

    // ── Diagnostics page ──────────────────────────────────────────────────────
    diagWidgets: [],
    diagGrid: null,
    diagActiveTab: 'graphs', // 'graphs' | 'utilities'

    // ── Ping utility ──────────────────────────────────────────────────────────
    pingHost: '',
    pingSource: '',
    pingCount: 5,
    pingSize: '',
    pingDf: false,
    pingTos: '',
    pingRunning: false,
    pingEventSource: null,
    pingRouterIfaces: [],
    terminalLines: [], // shared output terminal for all Utilities

    // ── Traceroute utility ────────────────────────────────────────────────────
    traceHost: '',
    traceSource: '',
    traceType: 'udp',
    traceRunning: false,
    traceEventSource: null,

    // ── Tcpdump utility ───────────────────────────────────────────────────────
    tcpdumpIface: '',
    tcpdumpFilter: '',
    tcpdumpSave: false,
    tcpdumpRunning: false,
    tcpdumpEventSource: null,
    tcpdumpPcapId: null,        // capture ID sent as first SSE event [captureid:<id>]
    tcpdumpDownloadReady: false, // true once file is finalized and ready to download

    // Tunnel Interfaces
    tunnelInterfaces: [],
    interfacesLoading: false,
    interfacesLoaded: false,
    interfacesError: '',
    interfaceLoadSeq: 0,
    selectedInterface: null,
    selectedInterfacePeers: [],
    selectedPeersLoading: false,
    selectedPeersLoaded: false,
    selectedPeersError: '',
    peerRefreshSeq: 0,
    allPeers: [],            // dashboard: flat list of peers from all interfaces
    allPeersLoading: false,
    allPeersLoaded: false,
    allPeersError: '',
    allPeerRefreshSeq: 0,
    showInterfaceCreate: false,
    createMode: 'quick',        // 'quick' | 'manual' — controls which form is shown in the create modal
    importConfForm: { name: '', conf: '', fileName: '' },
    importConfMode: 'server', // 'server' (client hub) | 'uplink' (S2S)
    importConfWarning: '',
    showImportBackup: false,
    importBackupTab: 'cascade', // 'cascade' | 'awgeasy'
    importBackupForm: { json: '', listenPort: '', fileName: '' },
    showExportInterface: false,
    exportInterfaceId: null,
    exportInterfaceIncludePeers: true,
    showInterfaceEdit: false,
    interfaceEdit: {
      id: null,
      name: '',
      address: '',
      listenPort: '',
      disableRoutes: false,
      natDisabled: false,
      dns: '',
      publicHost: '',
      mtu: 0,
      mss: 0,
      kernelMtu: 0,
      protocol: 'amneziawg-3.1',
      selectedTemplateId: '',
      settings: {
        jc: 6, jmin: 10, jmax: 50,
        s1: 64, s2: 67, s3: 64, s4: 4,
        h1: '', h2: '', h3: '', h4: '',
        i1: '', i2: '', i3: '', i4: '', i5: '',
      },
    },
    showPeerCreate: false, // manual peer create modal
    showQuickPeerCreate: false, // quick peer create dialog
    peerMutationInFlight: false,
    interfaceMutationInFlight: false,
    loadingInterfaceId: null,
    interfaceCreate: {
      name: '',
      protocol: 'amneziawg-3.1',
      address: '',
      listenPort: '',
      disableRoutes: false,
      dns: '',
      selectedTemplateId: '',   // UI-only template selection; not sent to the API
      settings: {
        jc: 6, jmin: 10, jmax: 50,
        s1: 64, s2: 67, s3: 64, s4: 4,
        h1: '', h2: '', h3: '', h4: '',
        i1: '', i2: '', i3: '', i4: '', i5: '',
        headerProtectionKey: '', contentPaddingAddition: '10-100',
        rekeyAfterTime: '100-120', rekeyTimeout: '3-7', rejectAfterTime: '150-180',
        keepaliveTimeout: '5-15', maxHandshakeAttempts: '15-20',
        randomTrailers: true, disableCookies: true,
      },
    },
    peerCreate: {
      mode: 'generate',     // 'generate' | 'manual'
      peerType: 'client',   // 'client' | 'interconnect'
      name: '',
      publicKey: '',
      endpoint: '',
      allowedIPs: '',
      clientAllowedIPs: '',
      persistentKeepalive: 25,
      groupId: '',          // client-group alias ID
      expiredAt: '',        // YYYY-MM-DD or empty
      showQR: false,
    },

    // Quick peer create (one-click)
    peerCreateName: '',
    peerCreateShowQR: false,
    inlineGroupInput: '',        // inline new-group name input value
    inlineGroupShow: false,      // show inline input in Manual Create modal
    inlineGroupShowQuick: false, // show inline input in Quick Create modal
    peerCreateExpiredDate: '',
    peerCreateGroupId: '',    // selected client-group for quick create

    // Peer management (inline editing, admin-tunnel style)
    peersPersist: {},
    peerDelete: null,      // peer for delete confirmation modal
    interfaceDelete: null, // interface for delete confirmation modal
    peerEditNameId: null,
    peerEditName: null,
    peerEditAddressId: null,
    peerEditAddress: null,
    peerEditExpireDateId: null,
    peerEditExpireDate: null,
    // Peer Edit Modal
    showPeerEditModal: false,
    peerEditForm: {
      _peer: null,          // original peer reference (for type checks + API call)
      name: '',
      persistentKeepalive: 0,
      endpoint: '',         // interconnect only
      allowedIPs: '',       // interconnect only (editable)
      clientAllowedIPs: '', // client only
      rateDown: 0,          // kbps, 0 = unlimited
      rateUp: 0,            // kbps, 0 = unlimited
      groupId: '',          // client-group alias ID
      expiredAt: '',        // Local YYYY-MM-DDTHH:mm or empty
    },
    // Settings
    globalSettings: {
      dns: '1.1.1.1, 8.8.8.8',
      defaultPersistentKeepalive: 25,
      defaultClientAllowedIPs: '0.0.0.0/0, ::/0',
      subnetPool:       '10.10.0.0/16',
      portPool:         '51831-65535',
      defaultFwPolicy:  'accept',
      gatewayWindowSeconds:     60,
      gatewayHealthyThreshold:  95,
      gatewayDegradedThreshold: 90,
      // Router identity
      routerName:     '',
      publicIPMode:   'auto',
      publicIPManual: '',
      // MTU for client configs (0 = auto)
      mtu: 0,
      // Expired peer policy
      expiredPeerPolicy:   'disable',  // "disable" | "restrict"
      expiredPeerRateDown: 0,          // kbps downstream; 0 = no limit
      expiredPeerRateUp:   0,          // kbps upstream;   0 = no limit
      expiredPeerGroupId:  '',         // client-group alias ID; '' = don't move
      // UI preferences
      lang: 'en',
      // Runtime-only (returned by GET, not sent in PUT)
      hostname:         '',
      resolvedPublicIP: '',
      publicIPWarning:  '',
      awgEngineVersion: '', awgToolsVersion: '', awgMaxProtocol: '2.0',
      awg3Supported: false, awg3SupportError: '',
    },
    settingsSaved: false,
    metricsSettings: {
      enabled: false,
      port: 9351,
      path: '/metrics',
      listening: false,
      listenError: '',
      connectedPeerThresholdSeconds: 180,
      tokenConfigured: false,
      historyEnabled: true,
      canManage: false,
      token: '',
      clearToken: false,
    },
    metricsSettingsSaved: false,
    templates: [],
    showTemplateModal: false,
    templateEditTarget: null, // null = create, object = edit
    templateForm: {
      name: '',
      isDefault: false,
      protocolVersion: '3.1',
      host: '',
      jc: 6, jmin: 10, jmax: 50,
      s1: 64, s2: 67, s3: 64, s4: 4,
      h1: '', h2: '', h3: '', h4: '',
      i1: '', i2: '', i3: '', i4: '', i5: '',
      headerProtectionKey: '', contentPaddingAddition: '10-100',
      rekeyAfterTime: '100-120', rekeyTimeout: '3-7', rejectAfterTime: '150-180',
      keepaliveTimeout: '5-15', maxHandshakeAttempts: '15-20',
      randomTrailers: true, disableCookies: true,
    },

    // Generate AmneziaWG template modal
    showGenerateModal: false,
    generateForm: {
      profile: 'random',
      intensity: 'medium',
      host: '',
      browser: '',
      saveName: '',
      protocolVersion: '3.1',
    },
    generatedParams: null,
    generatingParams: false,
    savingGeneratedTemplate: false,
    generateProfiles: [],

    // Gateways
    gateways: [],
    gatewaysLoaded: false,
    gatewaysLoading: false,
    gatewaysError: '',
    gatewaysRefreshPromise: null,
    gatewaysRefreshPromiseKey: null,
    gatewayMutationInFlight: false,
    gatewayGroups: [],
    systemInterfaces: [],

    // ── Remote servers ──────────────────────────────────────────────────────
    remotes: [],
    activeRemoteId: null,   // null = local server; string = remote id
    localServerName: '',    // name of the local server, set once on login and never overwritten by remote settings
    showRemoteAdd: false,
    remoteAddForm: { name: '', url: '', mode: 'login', username: '', password: '', totpCode: '', token: '', skipTlsVerify: false },
    remoteAddError: '',
    remoteAddLoading: false,
    remoteAddNeedsTOTP: false,
    remoteTesting: {},   // { [id]: true } while test is in progress
    remoteTestResult: {}, // { [id]: 'ok' | 'error' }

    // ── Speed test ────────────────────────────────────────────────────────────
    showSpeedtest: false,
    speedtest: {
      fromId: '__local__',
      toId: '__local__',
      via: 'auto',      // 'auto' | 'internet' | 'tunnel'
      tunnelIp: '',     // manual override when via='tunnel'
      duration: 10,
      streams: 4,
    },
    speedtestDetectedTunnelIp: '',
    speedtestFromIfaces: [],   // active interfaces on "from" server (manual mode)
    speedtestToIfaces: [],     // active interfaces on "to" server (manual mode)
    speedtestFromIfaceId: '',  // selected interface id on "from" (manual mode)
    speedtestToIfaceId: '',    // selected interface id on "to" (manual mode, bind addr for iperf3 client)
    speedtestPingConfirm: false,     // show ping-failed confirmation
    speedtestPingConfirmMsg: '',     // message shown in confirmation
    speedtestPendingHost: '',        // host waiting for confirmation
    speedtestPendingVia: '',
    speedtestRunning: false,
    speedtestResult: null,
    speedtestError: '',
    speedtestHistory: [],

    wizardsExpanded: false,


    // ── Wizard: Simple Client VPN ─────────────────────────────────────────────
    wizardVPN: {
      step: 1,                // 1=protocol, 2=name, 3=dns+peer, 4=result
	  protocol: 'amneziawg',  // 'wireguard' | 'amneziawg'
      ifaceName: '',
      dns: '',
      peerName: 'My Device',
      running: false,
      error: '',
      ifaceId: '',
      peerId: '',
      qrUrl: '',
      ifaceAddr: '',
      ifacePort: 0,
    },

    wizardUplink: {
      step: 1,          // 1=import conf, 2=source, 3=destination, 4=options, 5=apply
      // step 1
      confText: '',
      confFileName: '',
      preview: null,    // parsed preview from /parse-conf
      ifaceName: '',
      // step 2
      selectedIfaceIds: [],
      createSrcAlias: true,
      srcAliasName: '',
      // inline create interface mini-form
      showInlineCreate: false,
      inlineIfaceName: '',
      inlineIfaceAddr: '',
      inlineIfacePort: '',
	  inlineIfaceProto: 'amneziawg-3.1',
      inlineIfaceCreating: false,
      // step 3
      dstType: 'all',   // 'all' | 'geo' | 'as'
      dstCountries: [],
      dstASN: '',
      dstNegate: false,
      dstAliasName: '',
      // step 4
      mssClamp: true,
      fallback: 'drop', // 'drop' | 'allow'
      gatewayName: '',
      fwRuleName: '',
      natRuleName: '',
      gatewayMonitorIP: '',
      // step 5 — apply state
      applying: false,
      steps: [],        // [{ label, status:'pending'|'running'|'ok'|'warn'|'error', detail }]
      // created object IDs for final screen
      createdIfaceId: '',
      createdSrcAliasId: '',
      createdDstAliasId: '',
      createdGatewayId: '',
      createdFwRuleId: '',
      createdNatRuleId: '',
      done: false,
      fatalError: '',
    },

    // ── Wizard: Cascade ↔ Cascade S2S ────────────────────────────────────────
    wizardS2S: {
      step: 1,           // 1=remote, 2=source, 3=destination, 4=options, 5=apply
      // step 1 — remote
      remotes: [],
      remoteId: '',
      showAddRemote: false,
      addRemoteName: '', addRemoteURL: '', addRemoteMode: 'password',
      addRemoteUser: '', addRemotePass: '', addRemoteToken: '',
      addRemoteSkipTLS: false, addRemoteLoading: false,
      // step 2 — source
      selectedIfaceIds: [],
      createSrcAlias: true,
      srcAliasName: '',
      // step 3 — destination
      dstType: 'all',
      dstCountries: [],
      dstASN: '',
      dstNegate: false,
      dstAliasName: '',
      // step 4 — options
	  protocol: 'amneziawg-3.1',
      mssClamp: true,
      fallback: 'drop',
      localIfaceName: '',
      remoteIfaceName: '',
      gatewayName: '',
      fwRuleName: '',
      // step 5 — apply
      applying: false,
      steps: [],
      createdLocalIfaceId: '',
      createdRemoteIfaceId: '',
      createdSrcAliasId: '',
      createdDstAliasId: '',
      createdGatewayId: '',
      createdFwRuleId: '',
      done: false,
      fatalError: '',
    },

    showGatewayCreate: false,
    showGatewayEdit:   false,
    showGroupCreate:   false,
    showGroupEdit:     false,
    gatewayCreate: {
      name: '',
      interface: '',
      gatewayIP: '',
      monitorAddress: '',
      monitor: true,
      monitorInterval: 5,
      windowSeconds: null,
      latencyThreshold: 500,
      monitorHttp: { enabled: false, url: '', expectedStatus: 200, interval: 10, timeout: 5 },
      monitorRule: 'icmp_only',
      description: '',
    },
    gatewayEdit: {
      id: null,
      name: '',
      interface: '',
      gatewayIP: '',
      monitorAddress: '',
      monitor: true,
      monitorInterval: 5,
      windowSeconds: null,
      latencyThreshold: 500,
      monitorHttp: { enabled: false, url: '', expectedStatus: 200, interval: 10, timeout: 5 },
      monitorRule: 'icmp_only',
      description: '',
    },
    groupCreate: { name: '', trigger: 'packetloss', description: '', gateways: [] },
    groupEdit:   { id: null, name: '', trigger: 'packetloss', description: '', gateways: [] },

    // Routing page
    activeRoutingTab: 'status',   // 'status' | 'static' | 'policy' | 'ospf'
    routingTable: 'main',         // Table for the Status tab
    routingTables: [],            // Tables from /etc/iproute2/rt_tables
    kernelRoutes: [],
    kernelRoutesError: '',
    kernelRoutesLoading: false,
    staticRoutes: [],
    routeTestIp: '',
    routeTestSrc: '',          // Optional source IP for the policy trace
    routeTestResult: null,
    routeTestMatchedRule: null, // { id, name, fwmark } | null
    routeTestSteps: [],         // Trace steps for diagnostics
    routeTestLoading: false,
    routeTestError: '',
    showRouteCreate: false,
    routeCreate: {
      description: '',
      destination: '',
      viaMode: 'manual',    // 'manual' | 'gateway' | 'group'
      gateway: '',          // manual next-hop IP
      dev: '',              // manual interface
      gatewayId: '',        // linked Gateway ID (viaMode=gateway)
      gatewayGroupId: '',   // linked GatewayGroup ID (viaMode=group)
      metric: '',
      table: 'main',
    },
    showRouteEdit: false,
    routeEdit: {
      id: '',
      description: '',
      destination: '',
      viaMode: 'manual',
      gateway: '',
      dev: '',
      gatewayId: '',
      gatewayGroupId: '',
      metric: '',
      table: 'main',
    },

    // NAT page
    activeNatTab: 'outbound',     // 'outbound' | 'portforward'
    natRules: [],                 // NAT rules
    natInterfaces: [],            // Host network interfaces
    natRulesLoading: false,
    // Port Forwarding (DNAT)
    dnatRules: [],
    dnatLoading: false,
    showDnatModal: false,
    dnatEditMode: false,
    dnatForm: { id: '', name: '', protocol: 'udp', inInterface: '', inPort: '', dest: '', destPort: '', masquerade: true, comment: '' },
    showNatRuleCreate: false,     // Rule creation modal
    showNatRuleEdit: false,       // Rule edit modal
    natRuleCreate: {
      name: '',
      sourceType: 'any',          // 'any' | 'subnet' | 'ip' | 'alias'
      sourceValue: '',            // Value when sourceType is subnet or ip
      sourceAliasId: '',          // Alias ID when sourceType is alias
      outInterface: '',
      type: 'MASQUERADE',         // 'MASQUERADE' | 'SNAT'
      toSource: '',               // Destination IP when type is SNAT
      comment: '',
    },
    natRuleEdit: {
      id: null,
      name: '',
      sourceType: 'any',
      sourceValue: '',
      sourceAliasId: '',
      outInterface: '',
      type: 'MASQUERADE',
      toSource: '',
      comment: '',
    },

    // Firewall Aliases page
    aliases: [],
    aliasesLoading: false,
    clientGroups: [],            // aliases of type client-group
    showAliasCreate: false,
    showAliasEdit: false,
    aliasCreate: {
      name: '',
      description: '',
      type: 'network',          // 'host' | 'network' | 'ipset' | 'group' | 'port' | 'port-group'
      entries: '',              // textarea: one entry per line (host/network/port)
      ipsetEntries: '',         // textarea: manual CIDRs for ipset type
      memberIds: [],            // Selected UUID members for group/port-group
      genSource: 'country',     // 'country' | 'asn' | 'asn-list'
      genCountry: '',
      genAsn: '',
      genAsnList: '',
      file: null,               // optional CIDR file for ipset (uploaded immediately after create)
      rateDown: 0,              // kbps; 0 = unlimited (client-group only)
      rateUp: 0,
    },
    aliasEdit: {
      id: null,
      name: '',
      description: '',
      type: 'network',
      entries: '',
      ipsetEntries: '',         // manual CIDRs for ipset type (pre-populated for small sets)
      ipsetEntriesLoading: false,
      ipsetEntriesLoaded: false,
      memberIds: [],            // Selected UUID members for group/port-group
      genSource: 'country',
      genCountry: '',
      genAsn: '',
      genAsnList: '',
      rateDown: 0,              // kbps; 0 = unlimited (client-group only)
      rateUp: 0,
    },
    // Country picker combobox state (shared — only one modal open at a time)
    countrySearch: '',          // text in the filter input
    showCountryDrop: false,     // dropdown visible
    countryPickerForm: null,    // 'create' | 'edit' — which form is active
    aliasGeneratingId: null,    // Alias ID currently being generated
    aliasGenerateJobId: null,
    aliasGenerateJobStatus: null,
    aliasTooltip: null,         // { id, alias, x, y } — hover tooltip state
    systemRestoring: false,     // true while restore request is in flight
    showBackupModal: false,     // password prompt for backup download
    backupPassword: '',
    backupPasswordConfirm: '',
    backupIncludeMetrics: false,
    backupDownloading: false,
    showRestorePasswordModal: false, // password prompt for encrypted restore
    restorePassword: '',
    restoreFile: null,          // File object pending restore after password entry
    // Restore preview / remap state
    showRestorePreviewModal: false,
    restorePreview: null,       // { backupIfaces, serverIfaces, needsRemap }
    restoreIfaceMap: {},        // { oldIface: newIface } selected by user
    restorePreviewLoading: false,
    // Pre-restore auto-backups list
    preRestoreBackups: [],
    preRestoreBackupsLoading: false,

    // Import client configs (restore private keys for imported peers)
    importClientConfigsLoading: false,
    showImportClientConfigsResult: false,
    importClientConfigsResult: null, // { matched, unmatched: [...] }

    // Firewall Rules (replaces the former PBR section)
    firewallRules: [],
    firewallRulesLoading: false,
    firewallPending: false,   // unapplied changes exist
    firewallApplying: false,  // Apply button loading state
    firewallInterfaces: [],
    showFirewallCreate: false,
    showSeparatorModal: false,
    separatorEditId: null,    // null = new, string = edit existing
    separatorEdit: { name: '', color: '' },
    separatorColors: [
      { value: '',         label: 'Default', swatch: '#6b7280' },
      { value: 'red',      label: 'Red',     swatch: '#ef4444' },
      { value: 'orange',   label: 'Orange',  swatch: '#f97316' },
      { value: 'yellow',   label: 'Yellow',  swatch: '#eab308' },
      { value: 'green',    label: 'Green',   swatch: '#22c55e' },
      { value: 'cyan',     label: 'Cyan',    swatch: '#06b6d4' },
      { value: 'blue',     label: 'Blue',    swatch: '#3b82f6' },
      { value: 'purple',   label: 'Purple',  swatch: '#a855f7' },
    ],
    showFirewallEdit: false,
    firewallCreate: {
      name: '',
      interface: 'any',
      protocol: 'any',
      source:      { type: 'any', aliasId: '', value: '', invert: false, portMode: '', port: '', portAliasId: '' },
      destination: { type: 'any', aliasId: '', value: '', invert: false, portMode: '', port: '', portAliasId: '' },
      action: 'accept',
      gatewayId: '',
      gatewayGroupId: '',
      useGroup: false,
      fallbackToDefault: false,
      log: false,
      comment: '',
    },
    firewallEdit: {
      id: null,
      name: '',
      interface: 'any',
      protocol: 'any',
      source:      { type: 'any', aliasId: '', value: '', invert: false, portMode: '', port: '', portAliasId: '' },
      destination: { type: 'any', aliasId: '', value: '', invert: false, portMode: '', port: '', portAliasId: '' },
      action: 'accept',
      gatewayId: '',
      gatewayGroupId: '',
      useGroup: false,
      fallbackToDefault: false,
      log: false,
      comment: '',
    },

    // Toast notifications
    toasts: [],

    uiTheme: localStorage.theme || 'auto',
    prefersDarkScheme: window.matchMedia('(prefers-color-scheme: dark)'),

    // ISO 3166-1 alpha-2 country list for the country picker combobox.
    countries: [
      {code:'AD',name:'Andorra'},{code:'AE',name:'United Arab Emirates'},{code:'AF',name:'Afghanistan'},
      {code:'AG',name:'Antigua and Barbuda'},{code:'AL',name:'Albania'},{code:'AM',name:'Armenia'},
      {code:'AO',name:'Angola'},{code:'AR',name:'Argentina'},{code:'AT',name:'Austria'},
      {code:'AU',name:'Australia'},{code:'AZ',name:'Azerbaijan'},{code:'BA',name:'Bosnia and Herzegovina'},
      {code:'BB',name:'Barbados'},{code:'BD',name:'Bangladesh'},{code:'BE',name:'Belgium'},
      {code:'BF',name:'Burkina Faso'},{code:'BG',name:'Bulgaria'},{code:'BH',name:'Bahrain'},
      {code:'BI',name:'Burundi'},{code:'BJ',name:'Benin'},{code:'BN',name:'Brunei'},
      {code:'BO',name:'Bolivia'},{code:'BR',name:'Brazil'},{code:'BS',name:'Bahamas'},
      {code:'BT',name:'Bhutan'},{code:'BW',name:'Botswana'},{code:'BY',name:'Belarus'},
      {code:'BZ',name:'Belize'},{code:'CA',name:'Canada'},{code:'CD',name:'DR Congo'},
      {code:'CF',name:'Central African Republic'},{code:'CG',name:'Republic of the Congo'},
      {code:'CH',name:'Switzerland'},{code:'CI',name:"Côte d'Ivoire"},{code:'CL',name:'Chile'},
      {code:'CM',name:'Cameroon'},{code:'CN',name:'China'},{code:'CO',name:'Colombia'},
      {code:'CR',name:'Costa Rica'},{code:'CU',name:'Cuba'},{code:'CV',name:'Cape Verde'},
      {code:'CY',name:'Cyprus'},{code:'CZ',name:'Czech Republic'},{code:'DE',name:'Germany'},
      {code:'DJ',name:'Djibouti'},{code:'DK',name:'Denmark'},{code:'DM',name:'Dominica'},
      {code:'DO',name:'Dominican Republic'},{code:'DZ',name:'Algeria'},{code:'EC',name:'Ecuador'},
      {code:'EE',name:'Estonia'},{code:'EG',name:'Egypt'},{code:'ER',name:'Eritrea'},
      {code:'ES',name:'Spain'},{code:'ET',name:'Ethiopia'},{code:'FI',name:'Finland'},
      {code:'FJ',name:'Fiji'},{code:'FM',name:'Micronesia'},{code:'FR',name:'France'},
      {code:'GA',name:'Gabon'},{code:'GB',name:'United Kingdom'},{code:'GD',name:'Grenada'},
      {code:'GE',name:'Georgia'},{code:'GH',name:'Ghana'},{code:'GM',name:'Gambia'},
      {code:'GN',name:'Guinea'},{code:'GQ',name:'Equatorial Guinea'},{code:'GR',name:'Greece'},
      {code:'GT',name:'Guatemala'},{code:'GW',name:'Guinea-Bissau'},{code:'GY',name:'Guyana'},
      {code:'HN',name:'Honduras'},{code:'HR',name:'Croatia'},{code:'HT',name:'Haiti'},
      {code:'HU',name:'Hungary'},{code:'ID',name:'Indonesia'},{code:'IE',name:'Ireland'},
      {code:'IL',name:'Israel'},{code:'IN',name:'India'},{code:'IQ',name:'Iraq'},
      {code:'IR',name:'Iran'},{code:'IS',name:'Iceland'},{code:'IT',name:'Italy'},
      {code:'JM',name:'Jamaica'},{code:'JO',name:'Jordan'},{code:'JP',name:'Japan'},
      {code:'KE',name:'Kenya'},{code:'KG',name:'Kyrgyzstan'},{code:'KH',name:'Cambodia'},
      {code:'KI',name:'Kiribati'},{code:'KM',name:'Comoros'},{code:'KN',name:'Saint Kitts and Nevis'},
      {code:'KP',name:'North Korea'},{code:'KR',name:'South Korea'},{code:'KW',name:'Kuwait'},
      {code:'KZ',name:'Kazakhstan'},{code:'LA',name:'Laos'},{code:'LB',name:'Lebanon'},
      {code:'LC',name:'Saint Lucia'},{code:'LI',name:'Liechtenstein'},{code:'LK',name:'Sri Lanka'},
      {code:'LR',name:'Liberia'},{code:'LS',name:'Lesotho'},{code:'LT',name:'Lithuania'},
      {code:'LU',name:'Luxembourg'},{code:'LV',name:'Latvia'},{code:'LY',name:'Libya'},
      {code:'MA',name:'Morocco'},{code:'MC',name:'Monaco'},{code:'MD',name:'Moldova'},
      {code:'ME',name:'Montenegro'},{code:'MG',name:'Madagascar'},{code:'MH',name:'Marshall Islands'},
      {code:'MK',name:'North Macedonia'},{code:'ML',name:'Mali'},{code:'MM',name:'Myanmar'},
      {code:'MN',name:'Mongolia'},{code:'MR',name:'Mauritania'},{code:'MT',name:'Malta'},
      {code:'MU',name:'Mauritius'},{code:'MV',name:'Maldives'},{code:'MW',name:'Malawi'},
      {code:'MX',name:'Mexico'},{code:'MY',name:'Malaysia'},{code:'MZ',name:'Mozambique'},
      {code:'NA',name:'Namibia'},{code:'NE',name:'Niger'},{code:'NG',name:'Nigeria'},
      {code:'NI',name:'Nicaragua'},{code:'NL',name:'Netherlands'},{code:'NO',name:'Norway'},
      {code:'NP',name:'Nepal'},{code:'NR',name:'Nauru'},{code:'NZ',name:'New Zealand'},
      {code:'OM',name:'Oman'},{code:'PA',name:'Panama'},{code:'PE',name:'Peru'},
      {code:'PG',name:'Papua New Guinea'},{code:'PH',name:'Philippines'},{code:'PK',name:'Pakistan'},
      {code:'PL',name:'Poland'},{code:'PT',name:'Portugal'},{code:'PW',name:'Palau'},
      {code:'PY',name:'Paraguay'},{code:'QA',name:'Qatar'},{code:'RO',name:'Romania'},
      {code:'RS',name:'Serbia'},{code:'RU',name:'Russia'},{code:'RW',name:'Rwanda'},
      {code:'SA',name:'Saudi Arabia'},{code:'SB',name:'Solomon Islands'},{code:'SC',name:'Seychelles'},
      {code:'SD',name:'Sudan'},{code:'SE',name:'Sweden'},{code:'SG',name:'Singapore'},
      {code:'SI',name:'Slovenia'},{code:'SK',name:'Slovakia'},{code:'SL',name:'Sierra Leone'},
      {code:'SM',name:'San Marino'},{code:'SN',name:'Senegal'},{code:'SO',name:'Somalia'},
      {code:'SR',name:'Suriname'},{code:'SS',name:'South Sudan'},{code:'ST',name:'São Tomé and Príncipe'},
      {code:'SV',name:'El Salvador'},{code:'SY',name:'Syria'},{code:'SZ',name:'Eswatini'},
      {code:'TD',name:'Chad'},{code:'TG',name:'Togo'},{code:'TH',name:'Thailand'},
      {code:'TJ',name:'Tajikistan'},{code:'TL',name:'Timor-Leste'},{code:'TM',name:'Turkmenistan'},
      {code:'TN',name:'Tunisia'},{code:'TO',name:'Tonga'},{code:'TR',name:'Turkey'},
      {code:'TT',name:'Trinidad and Tobago'},{code:'TV',name:'Tuvalu'},{code:'TZ',name:'Tanzania'},
      {code:'UA',name:'Ukraine'},{code:'UG',name:'Uganda'},{code:'US',name:'United States'},
      {code:'UY',name:'Uruguay'},{code:'UZ',name:'Uzbekistan'},{code:'VA',name:'Vatican City'},
      {code:'VC',name:'Saint Vincent and the Grenadines'},{code:'VE',name:'Venezuela'},
      {code:'VN',name:'Vietnam'},{code:'VU',name:'Vanuatu'},{code:'WS',name:'Samoa'},
      {code:'YE',name:'Yemen'},{code:'ZA',name:'South Africa'},{code:'ZM',name:'Zambia'},
      {code:'ZW',name:'Zimbabwe'},
    ],

  },
  methods: {
    bytes,
    ...commonMethods,
    ...notificationMethods,
    ...authMethods,
    ...clientMethods,
    ...navigationMethods,
    ...interfaceMethods,
    ...gatewayMethods,
    ...routingMethods,
    ...natMethods,
    ...dashboardMethods,
    ...diagnosticsMethods,
    ...aliasMethods,
    ...backupMethods,
    ...firewallMethods,
    ...peerMethods,
    ...pollerMethods,
    ...settingsMethods,
    ...wizardMethods,
  },
  filters: {
    bytes,
    timeago: (value) => {
      return timeago.format(value, i18n.locale);
    },
    expiredDateFormat: (value) => {
      if (value === null) return i18n.t('Permanent');
      const dateTime = new Date(value);
      const options = { year: 'numeric', month: 'long', day: 'numeric' };
      return dateTime.toLocaleDateString(i18n.locale, options);
    },
    expiredDateEditFormat: (value) => {
      if (value === null) return 'yyyy-MM-dd';
    },
  },
  mounted() {
    // Remove splash screen — Vue is mounted, content is ready
    const splash = document.getElementById('app-splash');
    if (splash) {
      // Hide immediately — Vue is mounted, content is ready.
      splash.classList.add('splash-hide');
      setTimeout(() => { if (splash.parentNode) splash.parentNode.removeChild(splash); }, 400);
    }

    if (this.prefersDarkScheme.addEventListener) {
      this.prefersDarkScheme.addEventListener('change', this.handlePrefersChange);
    } else {
      this.prefersDarkScheme.addListener(this.handlePrefersChange);
    }
    this.setTheme(this.uiTheme);

    this.compactMediaQuery = window.matchMedia('(max-width: 1023px)');
    this.isCompactViewport = this.compactMediaQuery.matches;
    if (this.compactMediaQuery.addEventListener) {
      this.compactMediaQuery.addEventListener('change', this.handleCompactViewportChange);
    } else {
      this.compactMediaQuery.addListener(this.handleCompactViewportChange);
    }
    document.addEventListener('keydown', this.handleGlobalKeydown);

    this.api = new API();

    // Auto-switch back to local when the remote becomes unreachable.
    // This fires when a proxy call returns 401 (token revoked/expired) or
    // 5xx (remote server or network error), preventing a confusing login
    // window after a remote goes down.
    this.api._onRemoteError = (status, path) => {
      if (!this.activeRemoteId) return; // already local, ignore
      const remoteName = (this.activeRemote && this.activeRemote.name) || 'Remote server';
      this.switchToLocal();
      const reason = status === 401 ? 'authentication failed' : `error ${status}`;
      this.showToast(`${remoteName} disconnected (${reason}). Switched back to local.`, 'error');
    };

    this.api.getSession()
      .then((session) => {
        this.authenticated = session.authenticated;
        this.requiresPassword = session.requiresPassword;
        // First run: no users → show setup modal (non-dismissible)
        if (!session.requiresPassword) this.showFirstRunSetup = true;
        // Do not call protected endpoints while the login screen is visible.
        // Apart from unnecessary 401 responses, dashboard preload used to turn
        // a port-forwarding error into a misleading green "error" toast.
        if (!session.authenticated) return;
        this.refresh().catch((err) => {
          this.showToast(err.message || err.toString(), 'error');
        });
        // Load tunnel interfaces at startup (default page); then populate dashboard immediately
        this.loadTunnelInterfaces().then(() => {
          if (!this.activeInterfaceId) this.refreshAllPeers();
        }).catch(console.error);
        // Load settings + templates at startup so they are available
        // on any page (e.g. "Obfuscation Profile" dropdown in Create Interface modal).
        this.loadSettings();
        // Load client groups at startup — needed for peer create/edit dropdowns.
        this.loadClientGroups();
        // Load users and current user for the Users section in Settings.
        this.loadUsers();
        this.loadCurrentUser();
        // Load registered remote servers.
        this.loadRemotes();
        // Load dashboard layout + start metrics poller.
        this.loadDashboard();
        this.metricsStartPoller();
      })
      .catch((err) => {
        this.showToast(err.message || err.toString(), 'error');
      });

    this.api.getRememberMeEnabled()
      .then((rememberMeEnabled) => {
        this.rememberMeEnabled = rememberMeEnabled;
      });

    // Version info — fetch immediately (unauthenticated endpoint) and refresh
    // every 24 h so the update badge appears without a page reload.
    this.loadVersionInfo();
    setInterval(() => this.loadVersionInfo(), 24 * 60 * 60 * 1000);

    this.startResourcePoller();

    // System info polling every 30s (dashboard only)
    setInterval(() => {
      if (!document.hidden && this.activePage === 'dashboard' && this.dashWidgets.some(w => w.type === 'server-info')) {
        this.loadSystemInfo();
      }
    }, 30000);

    this.api.getuiTrafficStats()
      .then((res) => {
        this.uiTrafficStats = res;
      })
      .catch(() => {
        this.uiTrafficStats = false;
      });

    this.api.getWGEnableOneTimeLinks()
      .then((res) => {
        this.enableOneTimeLinks = res;
      })
      .catch(() => {
        this.enableOneTimeLinks = false;
      });

    this.api.getUiSortClients()
      .then((res) => {
        this.enableSortClient = res;
      })
      .catch(() => {
        this.enableSortClient = false;
      });

    this.api.getWGEnableExpireTime()
      .then((res) => {
        this.enableExpireTime = res;
      })
      .catch(() => {
        this.enableExpireTime = false;
      });

    this.api.getAvatarSettings()
      .then((res) => {
        this.avatarSettings = res;
      })
      .catch(() => {
          this.avatarSettings = {
            'dicebear': null,
            'gravatar': false,
          };
      });

    Promise.resolve().then(async () => {
      const lang = await this.api.getLang();
      if (lang !== localStorage.getItem('lang') && i18n.availableLocales.includes(lang)) {
        localStorage.setItem('lang', lang);
        i18n.locale = lang;
      }

    }).catch((err) => console.error(err));
  },
  beforeDestroy() {
    this.stopResourcePoller();
    this.metricsStopPoller();
    if (this.prefersDarkScheme) {
      if (this.prefersDarkScheme.removeEventListener) {
        this.prefersDarkScheme.removeEventListener('change', this.handlePrefersChange);
      } else {
        this.prefersDarkScheme.removeListener(this.handlePrefersChange);
      }
    }
    if (this._resourceVisibilityHandler) {
      document.removeEventListener('visibilitychange', this._resourceVisibilityHandler);
      this._resourceVisibilityHandler = null;
    }
    if (this._metricsVisibilityHandler) {
      document.removeEventListener('visibilitychange', this._metricsVisibilityHandler);
      this._metricsVisibilityHandler = null;
    }
    document.removeEventListener('keydown', this.handleGlobalKeydown);
    if (this.compactMediaQuery) {
      if (this.compactMediaQuery.removeEventListener) {
        this.compactMediaQuery.removeEventListener('change', this.handleCompactViewportChange);
      } else {
        this.compactMediaQuery.removeListener(this.handleCompactViewportChange);
      }
      this.compactMediaQuery = null;
    }
  },
  watch: {
    // Update browser tab title whenever router name or hostname changes.
    pageTitle: {
      immediate: true,
      handler(val) {
        document.title = val;
      },
    },
    showRemoteAdd(val) {
      if (!val) {
        this.remoteAddNeedsTOTP = false;
        this.remoteAddError = '';
        this.remoteAddForm = { name: '', url: '', mode: 'login', username: '', password: '', totpCode: '', token: '' };
      }
    },
    activeInterfaceId(newId) {
      if (newId) {
        this.selectedInterface = this.currentInterface;
        this.selectedInterfacePeers = [];
        this.selectedPeersLoaded = false;
        this.selectedPeersError = '';
        this.refreshPeers();
      } else {
        this.selectedInterface = null;
        this.selectedInterfacePeers = [];
        this.selectedPeersLoaded = false;
        this.selectedPeersError = '';
        this.allPeers = [];
        this.allPeersLoaded = false;
        this.allPeersError = '';
        // Switch to dashboard — immediately load all peers
        this.refreshAllPeers();
      }
    },
  },
  computed: {
    // Browser tab title: routerName if set, otherwise hostname, otherwise 'Cascade'.
    pageTitle() {
      return this.globalSettings.routerName || this.globalSettings.hostname || 'Cascade';
    },

    activePageLabel() {
      const page = this.sidebarMenu.find(item => item.id === this.activePage);
      return page ? page.label : 'Cascade';
    },

    // The currently active remote record, or null if we're on the local server.
    activeRemote() {
      if (!this.activeRemoteId) return null;
      return this.remotes.find(r => r.id === this.activeRemoteId) || null;
    },

    // Label shown in the server switcher dropdown.
    activeServerLabel() {
      return this.activeRemote ? this.activeRemote.name : (this.localServerName || this.pageTitle || 'Local');
    },
    // MSS clamping mode derived from the sentinel int value in interfaceEdit.mss.
    // Used to drive the select dropdown in Edit Interface modal.
    mssMode() {
      if (this.interfaceEdit.mss === -1) return 'auto';
      if (this.interfaceEdit.mss > 0)   return 'manual';
      return 'disabled';
    },
    filteredCountries() {
      const q = (this.countrySearch || '').trim().toLowerCase();
      if (!q) return this.countries;
      return this.countries.filter(c =>
        c.name.toLowerCase().startsWith(q) || c.code.toLowerCase().startsWith(q)
      );
    },
    currentInterface() {
      if (!this.activeInterfaceId) return null;
      return this.tunnelInterfaces.find(i => i.id === this.activeInterfaceId) || null;
    },
    theme() {
      if (this.uiTheme === 'auto') {
        return this.prefersDarkScheme.matches ? 'dark' : 'light';
      }
      return this.uiTheme;
    },
  },
});
