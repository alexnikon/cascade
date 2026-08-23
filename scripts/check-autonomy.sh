#!/usr/bin/env bash
# Reject accidental source/runtime coupling to repositories inherited by the fork.
set -euo pipefail

fail=0

for required_file in LICENSE NOTICE THIRD_PARTY_NOTICES.md; do
  if [[ ! -s "$required_file" ]]; then
    echo "Required licensing file is missing or empty: $required_file" >&2
    fail=1
  fi
done

if [[ -s LICENSE ]]; then
  grep -qx 'MIT License' <(head -n 1 LICENSE) || {
    echo "LICENSE must contain the canonical MIT License." >&2
    fail=1
  }
  grep -q 'Copyright (c) 2026 Alex Nikonov' LICENSE || {
    echo "LICENSE must identify the Cascade copyright owner." >&2
    fail=1
  }
fi

grep -q '\[MIT License\](LICENSE)' README.md || {
  echo "README must link to the MIT License." >&2
  fail=1
}
grep -q 'granted permission to distribute that inherited code' NOTICE || {
  echo "NOTICE must record the inherited-code relicensing permission." >&2
  fail=1
}
grep -q 'Copyright (c) 2026 Vadim Khristenko' THIRD_PARTY_NOTICES.md || {
  echo "AmneziaWG Architect attribution is missing." >&2
  fail=1
}

if rg -n 'Creative Commons Attribution-NonCommercial|CC BY-NC-SA|GNU AFFERO GENERAL PUBLIC LICENSE|AGPL-3\.0' . \
  --hidden -g '!.git/**' -g '!data/**' -g '!scripts/check-autonomy.sh'; then
  echo "Repository contains a conflicting project-license statement." >&2
  fail=1
fi

while IFS= read -r match; do
  [[ -z "$match" ]] && continue
  case "$match" in
    NOTICE:*|./NOTICE:*|THIRD_PARTY_NOTICES.md:*|./THIRD_PARTY_NOTICES.md:*) ;;
    *) echo "Unexpected upstream repository reference: $match" >&2; fail=1 ;;
  esac
done < <(rg -n 'github\.com/JohnnyVBut/cascade|JohnnyVBut/cascade' . \
  --hidden -g '!.git/**' -g '!data/**' -g '!scripts/check-autonomy.sh' || true)

if rg -n 'wg-easy\.github\.io/wg-easy/changelog\.json' . \
  --hidden -g '!.git/**' -g '!data/**'; then
  echo "Legacy wg-easy changelog dependency is forbidden." >&2
  fail=1
fi

while IFS= read -r match; do
  [[ -z "$match" ]] && continue
  case "$match" in
    ./.env.example:*|./.github/*|./Dockerfile:*|./README.md:*|./docker-compose*.yml:*|./deploy/*|./docs/*|./internal/frontend/embed_test.go:*) ;;
    *) echo "Unexpected hard-coded official release coordinate: $match" >&2; fail=1 ;;
  esac
done < <(rg -n 'api\.github\.com/repos/alexnikon/cascade|ghcr\.io/alexnikon/cascade|github\.com/alexnikon/cascade/releases' . \
  --hidden -g '!.git/**' -g '!data/**' || true)

exit "$fail"
