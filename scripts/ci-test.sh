#!/usr/bin/env bash
# Run the platform-neutral test suite used by CI adapters.
set -euo pipefail

go test ./... -count=1 -timeout 180s
go test -race ./internal/tunnel ./internal/metrics -count=1 -timeout 180s
go test -race ./internal/api \
  -run '^(TestListAllPeersReturnsInterfaceMetadataAndSanitizedKeys|TestOneTimeLinkReloadsAuthoritativePeerBeforeGeneratingConfig|TestBuildPeerRemoteConfigUsesAuthoritativePairWithoutCacheLookup|TestRemoteConfigGenerationFailureLeavesOneTimeTokenValid|TestOneTimeLinkConcurrentRedemptionSucceedsOnce|TestRepeatedPeerUpdateAndReloadPreserveCacheIdentity)$' \
  -count=1 -timeout 120s
