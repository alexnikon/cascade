#!/usr/bin/env bash
# Run the platform-neutral test suite used by CI adapters.
set -euo pipefail

go test ./... -count=1 -timeout 180s
go test -race ./internal/tunnel ./internal/metrics ./internal/dnsalias ./internal/webaccess -count=1 -timeout 180s
go test -race ./internal/api \
  -run '^(TestWebAccessGateRequiresVPNOrMachineToken|TestListAllPeersReturnsInterfaceMetadataAndSanitizedKeys|TestConfigDownloadUsesRestoredPrivateKey|TestBuildPeerRemoteConfigUsesAuthoritativePairWithoutCacheLookup|TestRepeatedPeerUpdateAndReloadPreserveCacheIdentity)$' \
  -count=1 -timeout 120s
