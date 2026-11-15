#!/usr/bin/env bash
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/release.sh"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

fail() { printf '%s\n' "$*" >&2; exit 1; }

for version in 0.0.0 1.2.3 v1.2.3 1.2.3-rc.1 1.2.3+build.01 1.2.3-0.1+build; do
  actual="$(bash "$script" resolve "$version")"
  [[ "$actual" == "v${version#v}" ]] || fail "Version changed: $version -> $actual"
done

# shellcheck disable=SC2016 # These are intentionally literal injection payloads.
for version in '' master v1.2 01.2.3 1.02.3 1.2.03 1.2.3-01 1.2.3-rc.01 1.2.3- 1.2.3+ 1.2.3-rc..1 '1.2.3; echo injected' '$(touch injected)' '"; touch injected; #' $'1.2.3\ntag=bad'; do
  if bash "$script" resolve "$version" > /dev/null 2>&1; then fail "Invalid version accepted: $version"; fi
done

# Exercise shell-injection-shaped input through the same environment boundary
# as the workflow. A marker command must never execute.
export RAW_VERSION="\$(touch '$scratch/injected')"
if bash -c 'bash "$1" resolve "$RAW_VERSION"' _ "$script" > /dev/null 2>&1; then fail 'Injection-shaped input accepted'; fi
[[ ! -e "$scratch/injected" ]] || fail 'Version input executed as shell code'

git init --bare -q "$scratch/remote.git"
git init -q "$scratch/work"
cd "$scratch/work"
git config user.name 'Release test'
git config user.email 'release-test@example.invalid'
git remote add origin "$scratch/remote.git"
git commit -q --allow-empty -m initial
first="$(git rev-parse HEAD)"

printf '1.2.3\n' > VERSION
printf '# Changelog\n\n## v1.2.3\n' > CHANGELOG.md
bash "$script" verify-source v1.2.3
if bash "$script" verify-source v1.2.4 > /dev/null 2>&1; then fail 'Mismatched source version accepted'; fi
printf '# Changelog\n\n## v1.2.30\n' > CHANGELOG.md
if bash "$script" verify-source v1.2.3 > /dev/null 2>&1; then fail 'Missing changelog entry accepted'; fi
printf '# Changelog\n\n## v1.2.3 — 2026-01-01\n' > CHANGELOG.md
bash "$script" verify-source v1.2.3
bash "$script" verify-tag v1.2.3
bash "$script" publish-tag v1.2.3 > /dev/null 2>&1
bash "$script" publish-tag v1.2.3 > /dev/null 2>&1
[[ "$(git --git-dir="$scratch/remote.git" rev-parse refs/tags/v1.2.3)" == "$first" ]] || fail 'Published tag targets wrong commit'
git tag -a v1.2.4 -m 'Annotated tag'
bash "$script" verify-tag v1.2.4

git commit -q --allow-empty -m second
if bash "$script" verify-tag v1.2.3 > /dev/null 2>&1; then fail 'Mismatched lightweight tag accepted'; fi
if bash "$script" verify-tag v1.2.4 > /dev/null 2>&1; then fail 'Mismatched annotated tag accepted'; fi
if bash "$script" publish-tag v1.2.3 > /dev/null 2>&1; then fail 'Mismatched tag published'; fi
[[ "$(git --git-dir="$scratch/remote.git" rev-parse refs/tags/v1.2.3)" == "$first" ]] || fail 'Existing tag moved'

printf 'Release tooling tests passed\n'
