# UI localization

Cascade's interface is a Vue 2 app whose strings are resolved through VueI18n.
English is the source of truth; other languages are added alongside it, and
`fallbackLocale` is `en`, so a string that is missing from a translation
catalogue renders in English rather than breaking the layout.

## Layout

| File | Role |
|---|---|
| `messages.en.json` | source catalogue — the English wording, one line per string |
| `messages.<locale>.json` | translation catalogue, same keys |
| `merge.py` | nests the flat catalogues and writes `../www/js/i18n.generated.js` |
| `../www/js/i18n.generated.js` | generated, embedded into the binary — **do not edit** |
| `../www/js/i18n.errors.js` | translations for backend error messages (see below) |

Keys are flat and namespaced: `namespace.name` (for example `peer.add`,
`settings.windowSec`). `merge.py` turns them into the nested objects VueI18n
expects, so the JSON stays reviewable — one diff line per string.

## Using a string

Templates (`internal/frontend/templates/**/*.html`):

```html
<p>{{$t("peer.add")}}</p>
<input :placeholder="$t('peer.namePlaceholder')">
```

Only these attributes are user-visible and may be bound for translation:
`title`, `placeholder`, `aria-label`, `alt` (and `label`).

JavaScript (`internal/frontend/www/js/*.js`): the feature modules export method
objects that are mixed into the root Vue instance, so `this` is the Vue instance
inside a method:

```js
this.showToast(this.$t('toast.interfaceSaved'));
```

`this.$t` is **not** available inside the `data:` initialiser. If a label
belongs there, expose it from `computed:` instead — see `sidebarMenu` and
`dashboardWidgetTypes` in `www/js/app.js`.

## Adding or changing a string

1. Add the key to `messages.en.json` with the exact English wording.
2. Add the same key to each `messages.<locale>.json`.
3. Reference it with `$t('namespace.name')`.
4. Run `python3 internal/frontend/i18n/merge.py`.
5. Run `go test ./internal/frontend/`.

`merge.py` fails if a locale is missing a key, has an extra key, or has an empty
value, so the catalogues cannot silently drift apart.

## Adding a language

1. Add `messages.<code>.json` with the same keys as `messages.en.json`.
2. Add `<code>` to the selector in `templates/views/settings.html`
   (`[{code:'en',label:'English'},{code:'ru',label:'Русский'}]`).
3. Run `merge.py`.

The backend validates the stored `lang` setting against the supported set in
`internal/settings/settings.go` — extend that list too.

## What not to translate

Protocol names, configuration keys and machine values stay Latin, embedded in
the translated sentence. This is deliberate: these tokens are copied into
configs, matched by parsers, or compared in code.

- Products and projects: Cascade, Caddy, Docker, WireGuard, AmneziaWG,
  Prometheus, Let's Encrypt, Grafana
- Protocols and network terms: NAT, DNAT, SNAT, MASQUERADE, PBR, ipset,
  iptables, CIDR, MTU, MSS, TUN, QUIC, HTTP/3, TLS, BBR, DNS, VPN, S2S
- AmneziaWG/WireGuard keys: `PrivateKey`, `PublicKey`, `PresharedKey`,
  `ListenPort`, `Address`, `AllowedIPs`, `Endpoint`, `PersistentKeepalive`,
  `PostUp`, `PostDown`, `Table`, `Jc`, `Jmin`, `Jmax`, `S1`–`S4`, `H1`–`H4`
- Commands, paths and identifiers: `awg show`, `ip rule`, `/etc/wireguard`,
  interface names such as `wg10`
- Units: ms, s, B, KB, MB, GB, Kbps, Mbps, %
- Firewall keywords: `ACCEPT`, `DROP`, `REJECT`
- Values compared in code: status values (`up`, `down`, `healthy`, `degraded`),
  protocol selectors (`tcp`, `udp`, `any`, `icmp`), HTTP verbs, mode strings
- Example values in placeholders: `10.0.0.0/24`, `51820`, `wg10`, `AS12345`

When in doubt, leave it in English — the fallback renders correctly, whereas a
wrong translation changes behaviour.

## Backend error messages

The Go handlers return English messages (`fiber.NewError`), and `www/js/api.js`
surfaces them verbatim in toasts. Rather than teaching the backend about
locales, `www/js/i18n.errors.js` maps the known messages to the active language
and passes anything unmatched through unchanged. Add entries there when you add
user-facing backend errors.

## Notes

- `merge.py` only needs the Python standard library; the frontend has no Node
  build step and vendors Vue/VueI18n under `www/js/vendor/`.
- `www/js/i18n.generated.js` is generated but committed, because the frontend is
  embedded into the Go binary at compile time and is served as-is.
