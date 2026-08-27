package frontend

import (
	"bytes"
	"encoding/json"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestFrontendDefinesEveryDarkTextUtility(t *testing.T) {
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	cssContent, err := assets.ReadFile("www/css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}

	classes := regexp.MustCompile(`dark:text-[A-Za-z0-9-]+`).FindAllString(string(indexContent), -1)
	seen := make(map[string]struct{}, len(classes))
	for _, className := range classes {
		if _, ok := seen[className]; ok {
			continue
		}
		seen[className] = struct{}{}
		selector := "." + strings.ReplaceAll(className, ":", `\:`) + ":where"
		if !strings.Contains(string(cssContent), selector) {
			t.Errorf("app.css does not define %s", className)
		}
	}
}

func TestEmbeddedAPIGuideUsesCanonicalEnglishDocument(t *testing.T) {
	viewerContent, err := assets.ReadFile("www/docs/api-viewer.html")
	if err != nil {
		t.Fatalf("read API viewer: %v", err)
	}
	viewer := string(viewerContent)
	if !strings.Contains(viewer, `fetch('./API.md')`) {
		t.Fatal("API viewer does not load canonical API.md")
	}
	for _, obsolete := range []string{"lang-toggle", "setLang", "getLang", "btn-ru"} {
		if strings.Contains(viewer, obsolete) {
			t.Errorf("API viewer still contains obsolete language selector symbol %q", obsolete)
		}
	}
	if _, err := assets.ReadFile("www/docs/API.md"); err != nil {
		t.Fatalf("read canonical embedded API.md: %v", err)
	}
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(indexContent), `href="./docs/api-viewer.html"`) {
		t.Fatal("frontend does not link to the canonical API viewer URL")
	}
}

func TestSafariChromeThemeUsesSingleRuntimeMeta(t *testing.T) {
	content, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	index := string(content)
	if got := strings.Count(index, `<meta name="theme-color"`); got != 1 {
		t.Fatalf("theme-color meta count = %d, want 1", got)
	}
	for _, expected := range []string{
		`<meta name="color-scheme" content="light dark">`,
		`root.style.colorScheme = dark ? 'dark' : 'light';`,
		`root.style.setProperty('--browser-chrome-color', chromeColor);`,
		`statusBar.setAttribute('content', dark ? 'black-translucent' : 'default');`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("index.html does not contain Safari chrome theme contract %q", expected)
		}
	}
}

func TestFrontendDarkThemeHasReadableTextDefaults(t *testing.T) {
	content, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	index := string(content)
	for _, expected := range []string{
		".dark .app-main-content,",
		".dark .modal-panel,",
		"color: #e5e7eb;",
		`class="text-xl font-semibold dark:text-neutral-100">Dashboard`,
		`text-orange-600 dark:text-orange-400`,
		`text-red-600 dark:text-red-400`,
		`:class="theme === 'dark' ? (toast.type === 'error' ? 'toast-error' : 'toast-success') : ''"`,
		`theme === 'dark' ? 'padding:4px 20px; color:#d4d4d4;'`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("index.html does not contain dark-theme safeguard %q", expected)
		}
	}
}

func TestFrontendNavigationThemeAndDashboardDefaults(t *testing.T) {
	appContent, err := assets.ReadFile("www/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	i18nContent, err := assets.ReadFile("www/js/i18n.js")
	if err != nil {
		t.Fatalf("read i18n.js: %v", err)
	}
	app, index, translations := string(appContent), string(indexContent), string(i18nContent)

	for _, expected := range []string{
		`const themes = ['light', 'dark', 'auto'];`,
		`this.uiTheme = themes[(currentIndex + 1) % themes.length];`,
		`subnetPool:       '10.10.0.0/16'`,
		`gatewayWindowSeconds:     60`,
		`{ id: 'w-server-info',   type: 'server-info',   x: 0, y: 0, w: 4, h: 4 }`,
		`{ id: 'w-gateways',      type: 'gateways',       x: 4, y: 0, w: 4, h: 4 }`,
		`{ id: 'w-output-traffic', type: 'monitoring',     x: 8, y: 0, w: 4, h: 4, graphs: [], title: 'Output Traffic', period: '5m' }`,
		`{ id: 'w-interfaces',    type: 'interfaces',     x: 0, y: 4, w: 6, h: 5 }`,
		`{ id: 'w-peers',         type: 'peers',          x: 6, y: 4, w: 6, h: 9 }`,
		`{ id: 'w-peers-summary', type: 'peers-summary',  x: 0, y: 9, w: 3, h: 4 }`,
		`{ id: 'w-traffic',       type: 'traffic',        x: 3, y: 9, w: 3, h: 4 }`,
	} {
		if !strings.Contains(app, expected) {
			t.Errorf("app.js does not contain %q", expected)
		}
	}

	for _, expected := range []string{
		`class="mobile-nav-close-icon"`,
		`.mobile-nav-close-icon {`,
		`width: 30px;`,
		`height: 30px;`,
		`placeholder="10.10.0.0/16"`,
		`(e.g. 10.10.0.1/24)`,
		`>Window (sec)</label>`,
		`:aria-label="$t(` + "`" + `theme.${uiTheme}` + "`" + `)"`,
		`>{{ uiTheme }}</span>`,
		`.dashboard-toolbar {`,
		`position: relative;`,
		`.dashboard-scroll {`,
		`class="dashboard-toolbar flex items-center justify-between"`,
		`class="grid-stack" id="dashboard-grid"`,
		`'dashboard-active': activePage === 'dashboard'`,
		`'dashboard-main-content': activePage === 'dashboard'`,
		`.app-main-content.dashboard-main-content { padding-inline: 0; }`,
		`class="dashboard-page"`,
		`class="dashboard-scroll"`,
		`{{ activePage === 'dashboard' ? 'Cascade' : activePageLabel }}`,
		`class="app-nav-divider"`,
		`style="padding: 8px 8px 0;" class="app-nav-menu"`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("index.html does not contain %q", expected)
		}
	}
	if strings.Contains(index, ".dashboard-toolbar {\n    position: sticky;") {
		t.Error("dashboard toolbar still uses scroll-reactive sticky positioning")
	}

	for _, obsolete := range []string{
		"UI_CHART_TYPES", "CHART_COLORS", "uiChartType", "uiShowCharts",
		"toggleCharts", "updateCharts", "transferTxSeries", "transferRxSeries",
	} {
		if strings.Contains(app, obsolete) || strings.Contains(index, obsolete) || strings.Contains(translations, obsolete) {
			t.Errorf("legacy peer chart symbol %q is still embedded", obsolete)
		}
	}
	if strings.Contains(index, "Traffic Charts") {
		t.Error("legacy Traffic Charts setting is still embedded")
	}
	if got := strings.Count(app, `breakpoints: [{ w: 1023, c: 1, layout: 'list' }]`); got != 2 {
		t.Errorf("responsive GridStack breakpoint count = %d, want 2", got)
	}
	if strings.Contains(app, `breakpoints: [{ w: 1024, c: 1, layout: 'list' }]`) {
		t.Error("GridStack still switches to compact layout at the desktop 1024px boundary")
	}
}

func TestFrontendLoadsDashboardPeersImmediatelyAfterLogin(t *testing.T) {
	appContent, err := assets.ReadFile("www/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	app := string(appContent)

	loginStart := strings.Index(app, "async _onLoginSuccess() {")
	if loginStart == -1 {
		t.Fatal("app.js does not define _onLoginSuccess")
	}
	loginEnd := strings.Index(app[loginStart:], "\n    logout(e) {")
	if loginEnd == -1 {
		t.Fatal("app.js does not delimit _onLoginSuccess")
	}
	loginFlow := app[loginStart : loginStart+loginEnd]
	for _, expected := range []string{
		"this.loadTunnelInterfaces().then(() => {",
		"if (!this.activeInterfaceId) this.refreshAllPeers();",
	} {
		if !strings.Contains(loginFlow, expected) {
			t.Errorf("post-login flow does not contain %q", expected)
		}
	}
}

func TestFrontendUsesConsistentButtonRadius(t *testing.T) {
	cssContent, err := assets.ReadFile("www/css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	css, index := string(cssContent), string(indexContent)
	if !strings.Contains(css, "button,\n.btn {") ||
		!strings.Contains(css, "  border-radius: 0.5rem !important;") {
		t.Error("app.css does not enforce the System Backup button radius")
	}
	if !strings.Contains(css, "button.rounded-full,\n.btn.rounded-full {\n  border-radius: 9999px !important;") {
		t.Error("app.css does not preserve fully rounded controls")
	}
	for _, radius := range []string{"4px", "5px", "7px"} {
		compact := `button[style*="border-radius:` + radius + `"],`
		spaced := `button[style*="border-radius: ` + radius + `"] {` + "\n  border-radius: " + radius + " !important;"
		if !strings.Contains(css, compact) || !strings.Contains(css, spaced) {
			t.Errorf("app.css does not preserve intentional inline radius %s", radius)
		}
	}
	if !strings.Contains(css, "button.ui-tab-button {\n  border-radius: 0 !important;") {
		t.Error("app.css does not remove the radius from tab buttons")
	}
	if !strings.Contains(css, "button.interface-filter-button {\n  border-radius: 0.5rem !important;") {
		t.Error("app.css does not preserve the Interfaces filter radius")
	}
	if got := strings.Count(index, `class="interface-filter-button px-4 py-2 font-medium transition text-sm"`); got != 2 {
		t.Errorf("Interfaces filter button count = %d, want 2", got)
	}
	if got := strings.Count(index, `class="btn flex items-center gap-1 px-3 py-1.5 rounded-lg text-sm font-medium transition"`); got != 2 {
		t.Errorf("Interfaces primary action button count = %d, want 2", got)
	}
	for _, expected := range []string{
		`.awg-generator-segmented button.is-active {`,
		`.dark .awg-generator-segmented button.is-active {`,
		`.awg-generator-segmented button.is-active {`,
		`background: #16a34a;`,
		`border-radius: 7px;`,
		`@click="generateForm.protocolVersion='2.0'" :class="{ 'is-active': generateForm.protocolVersion === '2.0' }"`,
		`class="rounded-lg overflow-hidden dark:border-neutral-500"`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("index.html does not contain tab safeguard %q", expected)
		}
	}
}

func TestFrontendUserBadgesAndMobileInterfaceActions(t *testing.T) {
	cssContent, err := assets.ReadFile("www/css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	css, index := string(cssContent), string(indexContent)

	for _, expected := range []string{
		`class="user-identity"`,
		`class="user-identity-name"`,
		`class="user-badge user-badge-admin"`,
		`.user-identity {`,
		`align-items: center;`,
		`flex-wrap: wrap;`,
		`.user-badge {`,
		`border-radius: 9999px;`,
		`min-height: 24px;`,
		`style="gap:20px; margin-bottom:10px;"`,
		`class="interface-peer-actions flex md:block md:flex-shrink-0"`,
		`border-radius: 10px;`,
		`min-width: 44px;`,
		`height: 44px;`,
		`.interface-peer-action + .interface-peer-action {`,
		`role="button" tabindex="0" aria-label="Restore interface"`,
		`@keydown.enter.prevent="$refs.restoreInterfaceInput.click()"`,
		`@keydown.space.prevent="$refs.restoreInterfaceInput.click()"`,
		`ref="restoreInterfaceInput"`,
	} {
		if !strings.Contains(index, expected) && !strings.Contains(css, expected) {
			t.Errorf("frontend does not contain badge/action safeguard %q", expected)
		}
	}
	if got := strings.Count(index, `class="user-badge`); got != 3 {
		t.Errorf("user badge count = %d, want 3", got)
	}
	if got := strings.Count(index, `class="interface-peer-action`); got != 6 {
		t.Errorf("interface action class count = %d, want 6 including the group", got)
	}
	actionStart := strings.Index(index, `class="interface-peer-actions flex md:block md:flex-shrink-0"`)
	if actionStart < 0 {
		t.Fatal("Interfaces peer action group not found")
	}
	actionEnd := strings.Index(index[actionStart:], "<!-- Peer cards")
	if actionEnd < 0 {
		t.Fatal("Interfaces peer action group end not found")
	}
	actions := index[actionStart : actionStart+actionEnd]
	for _, obsolete := range []string{"max-md:border-r-0", "max-md:border-x-0", "max-md:border-l-0", "rounded-l-full", "rounded-r-full"} {
		if strings.Contains(actions, obsolete) {
			t.Errorf("Interfaces mobile action group still contains %q", obsolete)
		}
	}
}

func TestFrontendClientCardsDoNotRenderAvatars(t *testing.T) {
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	cssContent, err := assets.ReadFile("www/css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	index, css := string(indexContent), string(cssContent)

	for _, obsolete := range []string{"<!-- Avatar -->", `client.avatar`, `peer.avatar`} {
		if strings.Contains(index, obsolete) {
			t.Errorf("client card still contains avatar markup %q", obsolete)
		}
	}
	if got := strings.Count(index, `class="peer-online-indicator"`); got != 3 {
		t.Errorf("peer status indicator count = %d, want 3", got)
	}
	for _, expected := range []string{
		`.peer-online-indicator {`,
		`background: #22c55e;`,
		`height: 8px;`,
		`width: 8px;`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("app.css does not preserve connection status with %q", expected)
		}
	}
}

func TestInterfacesPeerStatusAndEditHover(t *testing.T) {
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	cssContent, err := assets.ReadFile("www/css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	index := string(indexContent)
	css := string(cssContent)
	interfacesStart := strings.Index(index, `<!-- Interfaces Page`)
	if interfacesStart == -1 {
		t.Fatal("Interfaces page marker not found")
	}
	interfaces := index[interfacesStart:]

	for expected, want := range map[string]int{
		`peer.enabled === true && peer.latestHandshakeAt`: 2,
		`? '#22c55e' : '#9ca3af'`:                         2,
		`peer.enabled === false ? 'Disabled'`:             2,
		`? 'Inactive' : 'Never connected'`:                2,
	} {
		if got := strings.Count(interfaces, expected); got != want {
			t.Errorf("Interfaces status expression %q count = %d, want %d", expected, got, want)
		}
	}

	editHover := regexp.MustCompile(`(?s)<button @click="openPeerEdit\(peer\)".{0,300}class="peer-action-button"`)
	if got := len(editHover.FindAllString(interfaces, -1)); got != 2 {
		t.Errorf("Interfaces Edit buttons in action groups = %d, want 2", got)
	}
	if strings.Contains(interfaces, `style="hover:background`) {
		t.Error("Interfaces still contains invalid inline hover style")
	}

	for markup, want := range map[string]int{
		`class="peer-row-controls text-gray-400 dark:text-neutral-400"`: 2,
		`class="peer-action-group"`:                                     2,
		`class="peer-action-button"`:                                    10,
	} {
		if got := strings.Count(interfaces, markup); got != want {
			t.Errorf("Interfaces action markup %q count = %d, want %d", markup, got, want)
		}
	}
	for _, expected := range []string{
		`.peer-row-controls {`,
		`flex-wrap: wrap;`,
		`.peer-action-group {`,
		`background: transparent;`,
		`grid-auto-columns: 44px;`,
		`grid-auto-flow: column;`,
		`.dark .peer-action-group {`,
		`.peer-action-button:not(:disabled):not(.is-disabled):hover {`,
		`background: linear-gradient(135deg, #b91c1c 0%, #991b1b 100%);`,
		`.peer-action-button:focus-visible {`,
		`pointer-events: none;`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("peer action CSS does not contain %q", expected)
		}
	}
	if got := strings.Count(interfaces, `class="border-t-2 border-b-2 border-transparent">{{peer.name}}</span>`); got != 0 {
		t.Errorf("Interfaces peer names with transparent border classes = %d, want 0", got)
	}
	if got := strings.Count(interfaces, `v-show="peerEditNameId !== peer.id">{{peer.name}}</span>`); got != 2 {
		t.Errorf("borderless Interfaces peer names = %d, want 2", got)
	}
}

func TestFrontendCreatedAPITokenModalContainsLongCredentials(t *testing.T) {
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	cssContent, err := assets.ReadFile("www/css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	index, css := string(indexContent), string(cssContent)
	modalStart := strings.Index(index, `v-if="showNewTokenModal"`)
	if modalStart < 0 {
		t.Fatal("created API token modal not found")
	}
	modalEnd := strings.Index(index[modalStart:], `<!-- ==================== Alias Hover Tooltip`)
	if modalEnd < 0 {
		t.Fatal("created API token modal end not found")
	}
	modal := index[modalStart : modalStart+modalEnd]

	for _, expected := range []string{
		`class="modal-panel modal-panel-md api-token-created-modal`,
		`class="api-token-copy-row"`,
		`class="api-token-value`,
		`class="api-token-usage`,
	} {
		if !strings.Contains(modal, expected) {
			t.Errorf("created API token modal does not contain %q", expected)
		}
	}
	for _, expected := range []string{
		`.api-token-copy-row {`,
		`grid-template-columns: minmax(0, 1fr) auto;`,
		`overflow-wrap: anywhere;`,
		`@media (max-width: 479px) {`,
		`min-height: 44px;`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("API token modal CSS does not contain %q", expected)
		}
	}
}

func TestFrontendBackupModalDimsLaterSettingsPanels(t *testing.T) {
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	cssContent, err := assets.ReadFile("www/css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	index, css := string(indexContent), string(cssContent)

	for _, expected := range []string{
		`:class="{ 'settings-backup-modal-open': showBackupModal }"`,
		`class="settings-backup-modal fixed inset-0`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("backup modal stacking safeguard does not contain %q", expected)
		}
	}
	for _, expected := range []string{
		`.settings-backup-modal-open > .shadow-md {`,
		`-webkit-backdrop-filter: none !important;`,
		`backdrop-filter: none !important;`,
		`.settings-backup-modal {`,
		`z-index: 1000;`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("backup modal stacking CSS does not contain %q", expected)
		}
	}
}

func TestFrontendDiagnosticsUtilitiesMobileLayout(t *testing.T) {
	cssContent, err := assets.ReadFile("www/css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	css, index := string(cssContent), string(indexContent)

	for _, expected := range []string{
		`class="diag-utilities-grid"`,
		`class="diag-utility-terminal ping-terminal rounded"`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("Diagnostics Utilities does not contain responsive hook %q", expected)
		}
	}
	for className, want := range map[string]int{
		`class="diag-utility-card rounded-lg"`: 3,
		`class="diag-utility-fields"`:          2,
		`class="diag-utility-actions"`:         3,
	} {
		if got := strings.Count(index, className); got != want {
			t.Errorf("Diagnostics Utilities hook %q count = %d, want %d", className, got, want)
		}
	}
	for _, expected := range []string{
		`@media (max-width: 767px) {`,
		`.diag-utilities-grid {`,
		`flex-direction: column;`,
		`.diag-utility-card {`,
		`flex: 1 1 auto !important;`,
		`.diag-utility-fields > div {`,
		`min-width: 0 !important;`,
		`.diag-utility-actions {`,
		`.diag-utility-terminal {`,
		`overflow-x: auto;`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("app.css does not contain mobile Utilities safeguard %q", expected)
		}
	}
}

func TestFrontendUsesSharedVisibilityAwarePoller(t *testing.T) {
	content, err := assets.ReadFile("www/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	app := string(content)
	for _, expected := range []string{
		"startResourcePoller()",
		"document.addEventListener('visibilitychange'",
		"this.resourcePollTick().catch(console.error)",
		"}, 5000);",
		"this.api.getAllTunnelPeers()",
	} {
		if !strings.Contains(app, expected) {
			t.Errorf("app.js does not contain %q", expected)
		}
	}
	if strings.Contains(app, "}, 1000);") {
		t.Error("app.js still contains a one-second polling interval")
	}
}

func TestFrontendAPIExposesAggregatePeers(t *testing.T) {
	content, err := assets.ReadFile("www/js/api.js")
	if err != nil {
		t.Fatalf("read api.js: %v", err)
	}
	api := string(content)
	if !strings.Contains(api, "async getAllTunnelPeers()") || !strings.Contains(api, "path: '/peers'") {
		t.Error("api.js does not expose the aggregate peer endpoint")
	}
}

func TestFrontendUsesManifestReleaseLinks(t *testing.T) {
	content, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	index := string(content)
	if !strings.Contains(index, `v-if="versionInfo.releaseURL" :href="versionInfo.releaseURL"`) {
		t.Error("index.html does not use the release URL supplied by the update manifest")
	}
	if strings.Contains(index, "https://github.com/alexnikon/cascade/releases") {
		t.Error("index.html contains a hard-coded release provider fallback")
	}
}

func TestFrontendContainsUIIssueRegressions(t *testing.T) {
	appContent, err := assets.ReadFile("www/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	app := string(appContent)
	index := string(indexContent)

	for _, expected := range []string{
		"dashSavePeersView(widgetId)",
		"dashResetPeersView(widgetId)",
		"dashPeersViewDirty(widgetId)",
		"peerFilter: state.iface",
		"peerSort: state.sort",
		"metricsPruneUnavailableGraphs(snap)",
		"copyPublicIP()",
		"position:'topRight'",
	} {
		if !strings.Contains(app, expected) {
			t.Errorf("app.js does not contain %q", expected)
		}
	}

	for _, expected := range []string{
		"Edit details",
		"Save & Close",
		"dashPeersViewDirty(w.id) ? dashSavePeersView(w.id) : dashResetPeersView(w.id)",
		"title=\"Copy public IP\"",
		"@click=\"peerEditForm.expiredAt = ''\"",
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("index.html does not contain %q", expected)
		}
	}
	if got := strings.Count(index, "@click=\"dismissToast(toast.id)\""); got != 1 {
		t.Errorf("toast dismiss control count = %d, want 1", got)
	}
}

func TestFrontendClientExpiryUsesLocalDateTime(t *testing.T) {
	appContent, err := assets.ReadFile("www/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	app := string(appContent)
	index := string(indexContent)
	section := func(startMarker, endMarker string) string {
		start := strings.Index(index, startMarker)
		if start == -1 {
			t.Fatalf("expiry form marker not found: %s", startMarker)
		}
		end := strings.Index(index[start:], endMarker)
		if end == -1 {
			t.Fatalf("expiry form marker not found: %s", endMarker)
		}
		return index[start : start+end]
	}
	quickCreate := section("<!-- Quick Peer Create Dialog -->", "<!-- Peer Delete Confirmation Dialog -->")
	peerEdit := section("<!-- Peer Edit Modal -->", "<!-- Delete Dialog -->")

	for name, form := range map[string]string{
		"New Client":  quickCreate,
		"Edit Client": peerEdit,
	} {
		if got := strings.Count(form, `type="datetime-local" step="60"`); got != 1 {
			t.Errorf("%s datetime-local inputs = %d, want 1", name, got)
		}
		if !strings.Contains(form, "Expiry date and time") {
			t.Errorf("%s does not label the expiry time", name)
		}
	}
	if strings.Contains(quickCreate, `v-model="peerCreateExpiredDate" type="date"`) {
		t.Error("New Client still contains a date-only expiry input")
	}
	if strings.Contains(peerEdit, `v-model="peerEditForm.expiredAt" type="date"`) {
		t.Error("Edit Client still contains a date-only expiry input")
	}

	for _, expected := range []string{
		"expiryDateTimeToUTC(value)",
		"expiryDateTimeForInput(value)",
		"expiredAt: expiredAt || undefined",
		"expiredAt: this.expiryDateTimeForInput(peer.expiredAt)",
		"updates.expiredAt = expiredAt",
		"Invalid expiry date and time",
	} {
		if !strings.Contains(app, expected) {
			t.Errorf("client expiry implementation does not contain %q", expected)
		}
	}
}

func TestDashboardEntityRowsAreReadOnly(t *testing.T) {
	appContent, err := assets.ReadFile("www/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	app := string(appContent)
	index := string(indexContent)
	dashboardSection := func(startMarker, endMarker string) string {
		start := strings.Index(index, startMarker)
		if start == -1 {
			t.Fatalf("dashboard marker not found: %s", startMarker)
		}
		end := strings.Index(index[start:], endMarker)
		if end == -1 {
			t.Fatalf("dashboard marker not found: %s", endMarker)
		}
		return index[start : start+end]
	}
	interfaces := dashboardSection("<!-- ── interfaces ── -->", "<!-- ── traffic ── -->")
	peers := dashboardSection("<!-- ── peers (full list with filter + controls) ── -->", "<!-- ── monitoring ── -->")

	for _, obsolete := range []string{
		"dashToggleInterface(iface)",
		"dashOpenAddPeer(iface)",
	} {
		if strings.Contains(app, obsolete) || strings.Contains(index, obsolete) {
			t.Errorf("dashboard still contains entity action %q", obsolete)
		}
	}
	for _, obsolete := range []string{
		"startTunnelInterface(iface)",
		"stopTunnelInterface(iface)",
		"openInterfaceEdit(iface)",
		"cursor:pointer",
	} {
		if strings.Contains(interfaces, obsolete) {
			t.Errorf("dashboard interfaces row still contains action %q", obsolete)
		}
	}
	for _, obsolete := range []string{
		"disablePeer(peer)",
		"enablePeer(peer)",
		"peerQrUrl(peer.interfaceId, peer.id)",
		"downloadPeerConfig(peer)",
		"showPeerOneTimeLink(peer)",
		"openPeerEdit(peer)",
		"peerDelete = peer",
	} {
		if strings.Contains(peers, obsolete) {
			t.Errorf("dashboard peers row still contains action %q", obsolete)
		}
	}

	for _, expected := range []string{
		`<span :title="iface.enabled ? 'Up' : 'Down'"`,
		`class="dash-peers-toolbar"`,
		`class="dash-peers-select"`,
		`class="dash-peers-view-action"`,
		`<!-- online status dot -->`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("read-only dashboard does not contain %q", expected)
		}
	}
	if got := strings.Count(peers, `class="dash-peers-view-action"`); got != 1 {
		t.Errorf("dashboard peers view action count = %d, want 1", got)
	}
	if strings.Contains(peers, "peerEffectiveRate(peer)") {
		t.Error("dashboard peers still contains the rate-limit badge")
	}

	cssContent, err := assets.ReadFile("www/css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(cssContent)
	for _, expected := range []string{
		`.dash-peers-panel {`,
		`overflow: hidden;`,
		`.dash-peers-toolbar {`,
		`grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) auto;`,
		`.dash-peers-select {`,
		`min-width: 0 !important;`,
		`.dash-peers-view-action.is-dirty {`,
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("dashboard peers responsive CSS does not contain %q", expected)
		}
	}
}

func TestFrontendExposesAllAmneziaTemplateVersions(t *testing.T) {
	appContent, err := assets.ReadFile("www/js/app.js")
	if err != nil {
		t.Fatal(err)
	}
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatal(err)
	}
	app, index := string(appContent), string(indexContent)
	for _, expected := range []string{"protocol: 'amneziawg-3.1'", "protocolVersion: '3.1'", "headerProtectionKey", "awg3Supported"} {
		if !strings.Contains(app, expected) {
			t.Errorf("app.js does not contain %q", expected)
		}
	}
	for _, expected := range []string{"Amnezia Templates", "Use predefined templates or create your own. Supported up to version AWG 3.1", "New template", `value="1.0"`, "AWG 1.0", "AWG 2.0", "AWG 3.1", "HeaderProtectionKey", `value="amneziawg-3.1"`} {
		if !strings.Contains(index, expected) {
			t.Errorf("index.html does not contain %q", expected)
		}
	}
	for _, unexpected := range []string{"AWG3 Templates", "New AWG 3.1 Template", "Legacy AWG 2.0", "legacy AWG 2.0"} {
		if strings.Contains(index, unexpected) {
			t.Errorf("index.html still contains %q", unexpected)
		}
	}
}

func TestFrontendLoginAndLayoutRegressions(t *testing.T) {
	appContent, err := assets.ReadFile("www/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	indexContent, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	app, index := string(appContent), string(indexContent)

	for _, expected := range []string{
		`id="login-form"`,
		`autocomplete="username"`,
		`autocomplete="current-password"`,
		`v-model="password"`,
		`id="totp-form"`,
		`v-model="totpCode" @keyup.enter="loginStep2"`,
		`maxlength="6" placeholder="000000"`,
		`type="button" @click="loginStep2"`,
		`class="app-nav-menu"`,
		`class="app-nav-footer"`,
		`class="flex app-shell"`,
		`flex flex-col app-nav`,
		`class="flex-grow app-main"`,
		`class="mobile-app-header"`,
		`aria-controls="app-navigation"`,
		`class="mobile-nav-backdrop"`,
		`:inert="isCompactViewport && !mobileNavOpen"`,
		`navigator.standalone === true`,
		`window.matchMedia('(display-mode: standalone)').matches`,
		`classList.toggle('pwa-standalone', standalone);`,
		`class="app-root"`,
		`html, body, #app, .app-root, .app-shell`,
		`html.pwa-standalone .app-root`,
		"html.pwa-standalone .app-shell {\n    position: fixed;",
		"height: 100vh;\n    min-height: 100vh;\n    max-height: 100vh;",
		`calc(24px + env(safe-area-inset-bottom))`,
		`calc(12px + env(safe-area-inset-bottom))`,
		`awg-generator-panel`,
		`awg-generator-footer`,
		`class="firewall-toolbar"`,
		`bottom:max(24px, env(safe-area-inset-bottom))`,
		`right:max(24px, env(safe-area-inset-right))`,
		`<meta name="color-scheme" content="light dark">`,
		`window.applyCascadeTheme = (theme, mediaQuery = systemScheme) => {`,
		`root.style.colorScheme = dark ? 'dark' : 'light';`,
		`root.style.setProperty('--browser-chrome-color', chromeColor);`,
		`statusBar.setAttribute('content', dark ? 'black-translucent' : 'default');`,
		`background-color: var(--browser-chrome-color, #f8fafc) !important;`,
		`window.applyCascadeTheme(selectedTheme, systemScheme);`,
	} {
		if !strings.Contains(index, expected) {
			t.Errorf("index.html does not contain %q", expected)
		}
	}
	if strings.Contains(index, "#app > div") {
		t.Error("root layout styles still target every direct child of #app")
	}
	for _, expected := range []string{
		`if (!session.authenticated) return;`,
		`this.showToast(e.message || 'Failed to load port forwarding rules', 'error')`,
		`username: '',          // login form username field`,
		`if (usernameInput) this.username = usernameInput.value.trim();`,
		`if (passwordInput) this.password = passwordInput.value;`,
		`if (!this.totpCode || this.authenticating) return;`,
		`mobileNavOpen: false`,
		`handleCompactViewportChange(event)`,
		`window.applyCascadeTheme(theme, this.prefersDarkScheme);`,
		`this.prefersDarkScheme.addEventListener('change', this.handlePrefersChange);`,
		`this.prefersDarkScheme.addListener(this.handlePrefersChange);`,
		`this.prefersDarkScheme.removeEventListener('change', this.handlePrefersChange);`,
		`this.prefersDarkScheme.removeListener(this.handlePrefersChange);`,
		`if (this.uiTheme === 'auto')`,
		`breakpoints: [{ w: 1023, c: 1, layout: 'list' }]`,
		`this._dashSaveEnabled = false;`,
		`this._diagSaveEnabled = false;`,
		`if (this.activePage === 'dashboard') {`,
		`this.loadDashboard();`,
		`if (this.activePage === 'diagnostics') {`,
		`this.loadDiagnostics();`,
		`el.className = 'grid-stack';`,
		`item.setAttribute('gs-w', widget.w);`,
		`item.setAttribute('gs-h', widget.h);`,
		`this._dashSaveEnabled && !this.isCompactViewport`,
		`this._diagSaveEnabled && !this.isCompactViewport`,
	} {
		if !strings.Contains(app, expected) {
			t.Errorf("app.js does not contain %q", expected)
		}
	}
	if strings.Contains(app, "this.showToast('error',") || strings.Contains(app, "this.showToast('success',") {
		t.Error("app.js contains a showToast call with reversed message/type arguments")
	}
	if strings.Contains(index, `id="totp-username"`) {
		t.Error("OTP form contains a username field that may trigger Safari credential autofill")
	}
	if strings.Contains(index, `id="totp-form" name="totp" method="post" autocomplete="off"`) {
		t.Error("OTP form disables autocomplete and may suppress one-time-code suggestions")
	}
	if strings.Contains(app, `username: this.username || 'admin'`) {
		t.Error("login still falls back to the admin username")
	}
	loginStepStart := strings.Index(app, "async loginStep2()")
	if loginStepStart < 0 {
		t.Fatal("loginStep2 implementation start not found")
	}
	loginStepEnd := strings.Index(app[loginStepStart:], "// Called after a successful full authentication")
	if loginStepEnd < 0 {
		t.Fatal("loginStep2 implementation boundaries not found")
	}
	loginStep := app[loginStepStart : loginStepStart+loginStepEnd]
	if !strings.Contains(loginStep, "this.totpCode = '';") {
		t.Error("loginStep2 does not clear the TOTP code after successful verification")
	}
	catchStart := strings.Index(loginStep, "} catch (err) {")
	if catchStart < 0 {
		t.Fatal("loginStep2 catch block not found")
	}
	if strings.Contains(loginStep[catchStart:], "this.totpCode = '';") {
		t.Error("loginStep2 clears the TOTP code after a failed verification")
	}
	for _, unexpected := range []string{
		`blurActiveAuthInput()`,
		`onTOTPInput(value)`,
		`this.totpCode.length !== 6`,
		`focusTOTPInput()`,
		`ref="totpCodeInput"`,
		`autocomplete="one-time-code"`,
	} {
		if strings.Contains(app, unexpected) || strings.Contains(index, unexpected) {
			t.Errorf("frontend still contains reverted TOTP behavior %q", unexpected)
		}
	}
	for _, unexpected := range []string{
		"scheduleLoginAfterAutofill",
		"onPasswordAutofill",
		"cascade-autofill-detected",
		"setTimeout(() => this.loginStep2()",
	} {
		if strings.Contains(app, unexpected) || strings.Contains(index, unexpected) {
			t.Errorf("frontend still contains automatic Safari submission hook %q", unexpected)
		}
	}
}

func TestFrontendFirstRunUsesLoginDesign(t *testing.T) {
	content, err := assets.ReadFile("www/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	index := string(content)
	start := strings.Index(index, "<!-- ==================== First-Run Setup ==================== -->")
	end := strings.Index(index, "<!-- ==================== /First-Run Setup ==================== -->")
	if start < 0 || end <= start {
		t.Fatal("first-run setup section not found")
	}
	firstRun := index[start:end]

	for _, expected := range []string{
		`class="auth-screen first-run-screen"`,
		`<form id="first-run-form" name="first-run" @submit.prevent="createFirstUser()"`,
		`class="shadow-xl bg-white dark:bg-neutral-800 auth-card"`,
		`class="auth-brand"`,
		`src="./img/cascade_favicon.svg" class="auth-brand-mark"`,
		`id="setup-title" class="auth-title"`,
		`class="auth-subtitle"`,
		`type="submit" :disabled="firstRunSaving || restorePreviewLoading || systemRestoring" class="auth-primary"`,
		`aria-label="Creating account"`,
		`class="auth-divider"`,
		`ref="firstRunRestoreInput" type="file" class="hidden"`,
		`accept=".tar.gz,.gz,.enc"`,
		`type="button" class="auth-secondary"`,
		`Restore from Backup`,
	} {
		if !strings.Contains(firstRun, expected) {
			t.Errorf("first-run setup does not contain shared auth design %q", expected)
		}
	}
	if got := strings.Count(firstRun, `class="auth-field"`); got != 3 {
		t.Errorf("first-run auth field count = %d, want 3", got)
	}
	for _, obsolete := range []string{`role="dialog"`, `shadow-2xl`, `bg-purple-600`, `<svg width="28"`} {
		if strings.Contains(firstRun, obsolete) {
			t.Errorf("first-run setup still contains obsolete design %q", obsolete)
		}
	}

	sharedStart := strings.Index(index, "<!-- ==================== Shared System Restore Modals ==================== -->")
	sharedEnd := strings.Index(index, "<!-- ==================== /Shared System Restore Modals ==================== -->")
	if sharedStart < 0 || sharedEnd <= sharedStart || sharedStart <= end {
		t.Fatal("shared restore modals are not rendered outside the first-run/main application branches")
	}
	shared := index[sharedStart:sharedEnd]
	for _, expected := range []string{
		`v-if="showRestorePasswordModal"`,
		`v-if="showRestorePreviewModal"`,
		`@click.self="cancelRestoreFlow()"`,
		`v-if="showFirstRunSetup"`,
		`Sign in with an administrator account from the backup after restart.`,
	} {
		if !strings.Contains(shared, expected) {
			t.Errorf("shared first-run restore flow does not contain %q", expected)
		}
	}
	settingsStart := strings.Index(index, "<!-- Settings Page")
	apiTokensStart := strings.Index(index, "<!-- API Tokens -->")
	if settingsStart < 0 || apiTokensStart <= settingsStart {
		t.Fatal("Settings section boundaries not found")
	}
	settingsRestoreArea := index[settingsStart:apiTokensStart]
	for _, duplicated := range []string{`v-if="showRestorePasswordModal"`, `v-if="showRestorePreviewModal"`} {
		if strings.Contains(settingsRestoreArea, duplicated) {
			t.Errorf("Settings still contains duplicated restore modal %q", duplicated)
		}
	}
}

func TestFrontendFirstRunRestoreKeepsEncryptedPasswordUntilApply(t *testing.T) {
	content, err := assets.ReadFile("www/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	app := string(content)
	for _, expected := range []string{
		`cancelRestoreFlow() {`,
		`const previewReady = await this._startRestorePreview(this.restoreFile, this.restorePassword);`,
		`if (previewReady) this.showRestorePasswordModal = false;`,
		`return true;`,
		`return false;`,
		`const password = this.restorePassword || '';`,
	} {
		if !strings.Contains(app, expected) {
			t.Errorf("encrypted first-run restore flow does not contain %q", expected)
		}
	}
	obsolete := `await this._startRestorePreview(this.restoreFile, this.restorePassword);\n      this.restorePassword = '';`
	if strings.Contains(app, obsolete) {
		t.Error("encrypted restore password is still cleared before apply")
	}
}

func TestPWAAssets(t *testing.T) {
	manifestContent, err := assets.ReadFile("www/manifest.json")
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}

	var manifest struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		ShortName       string `json:"short_name"`
		StartURL        string `json:"start_url"`
		Scope           string `json:"scope"`
		Display         string `json:"display"`
		BackgroundColor string `json:"background_color"`
		ThemeColor      string `json:"theme_color"`
		Icons           []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Type    string `json:"type"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatalf("decode manifest.json: %v", err)
	}
	if manifest.ID != "." || manifest.StartURL != "." || manifest.Scope != "." {
		t.Fatalf("manifest paths must remain relative to the hidden admin path: %+v", manifest)
	}
	if manifest.Name != "Cascade" || manifest.ShortName != "Cascade" || manifest.Display != "standalone" {
		t.Fatalf("unexpected PWA identity: %+v", manifest)
	}
	if manifest.BackgroundColor != "#0f172a" || manifest.ThemeColor != "#0f172a" {
		t.Fatalf("unexpected PWA colors: %+v", manifest)
	}

	wantIcons := map[string]string{
		"www/img/pwa-icon-192.png": "192x192",
		"www/img/pwa-icon-512.png": "512x512",
	}
	for assetPath, wantSize := range wantIcons {
		content, err := assets.ReadFile(assetPath)
		if err != nil {
			t.Fatalf("read %s: %v", assetPath, err)
		}
		config, err := png.DecodeConfig(bytes.NewReader(content))
		if err != nil {
			t.Fatalf("decode %s: %v", assetPath, err)
		}
		if config.Width != config.Height || wantSize != strings.Join([]string{
			strconv.Itoa(config.Width),
			strconv.Itoa(config.Height),
		}, "x") {
			t.Fatalf("%s has dimensions %dx%d, want %s", assetPath, config.Width, config.Height, wantSize)
		}
	}

	if len(manifest.Icons) != 2 {
		t.Fatalf("manifest has %d icons, want 2", len(manifest.Icons))
	}
	for _, icon := range manifest.Icons {
		if icon.Type != "image/png" || icon.Purpose != "any maskable" {
			t.Fatalf("unexpected manifest icon: %+v", icon)
		}
		if wantIcons["www/"+icon.Src] != icon.Sizes {
			t.Fatalf("manifest icon does not match an embedded asset: %+v", icon)
		}
	}

	touchIcon, err := assets.ReadFile("www/img/apple-touch-icon.png")
	if err != nil {
		t.Fatalf("read apple touch icon: %v", err)
	}
	touchConfig, err := png.DecodeConfig(bytes.NewReader(touchIcon))
	if err != nil {
		t.Fatalf("decode apple touch icon: %v", err)
	}
	if touchConfig.Width != 180 || touchConfig.Height != 180 {
		t.Fatalf("apple touch icon has dimensions %dx%d, want 180x180", touchConfig.Width, touchConfig.Height)
	}
}
