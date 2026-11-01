# Architect profile generator

Cascade's Go generator is based on Any-Tech-ARCHITECT 4.5.1,
commit `0b9d3b7aa6c1661daa29b92010f841a3c04ca9fb`. It runs without
Node, Bun, or calls to the Architect website. AWG 2.0 and 3.1 are supported.

`GET /api/templates/generator-options` exposes the pinned catalogues and
version presets. `POST /api/templates/generate` accepts the generator controls,
returns the resolved profile, effective options, MTU and compatibility warnings,
and optionally saves a template through `saveName`. Existing `host`, `browser`,
`jc`, and `iterCount` request fields remain supported. `junkLevel` overrides `jc`.
Explicit boolean `false` and `junkLevel: 0` do not select defaults.

The generator's `mtu` field is labelled **CPS packet size budget (bytes)** in the
UI. It sizes generated CPS packets and is stored with the template as generation
metadata; editing this metadata does not resize existing packet chains. It does
not override the MTU of a new interface: generation, protocol changes and template
selection preserve the interface form's current value. New interfaces start with
`MTU = 0` (auto), which omits the MTU directive from the server configuration and
lets wg-quick/awg-quick calculate it. An explicitly entered nonzero interface MTU
is retained. Existing interfaces and the global client-config MTU are unchanged.

## Compatibility differences

- Cascade generates and stores the shared HeaderProtectionKey for Amnezia VPN.
  The app must use that same key rather than generate an independent one.
- The `<c>` tag is filtered against both the chosen client and Cascade's current
  server engine. The userspace server does not support it.
- Client parser limits do not prove support for every AWG 3.1 extension in every
  release. The UI and generated warnings explain that limitation.
- Optional AWG 3.1 strings are omitted when disabled; explicit boolean flags are
  retained. Both tunnel ends receive the same shared transport settings.

## Updating the pinned port

Use Node 24+ and a clean checkout of the pinned Architect revision:

```sh
node scripts/update-architect-data.mjs ../Any-Tech-ARCHITECT --fixtures
go test ./internal/awgparams ./internal/settings ./internal/api ./internal/db
node --test internal/frontend/*_test.mjs
```

The export script evaluates the original TypeScript modules without installing
frontend dependencies. It copies client/build limits, browser sizes, English
notes and protocol/region-specific host pools, including Architect's fallbacks.
It also exports 168 reference cases using a zero-filled test entropy source and
a fixed clock. This test entropy is never used by production generation.

For a future source version, review and port the algorithm changes before
changing the revision in the exporter, generator constants and notices. Update
packet-framing and randomized client/profile invariant tests as needed. The Go
port follows the upstream kernel chain budget and packet-size collision rules.
