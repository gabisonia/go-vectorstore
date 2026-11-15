#!/usr/bin/env bash
set -euo pipefail

resolve_version() {
  local version="${1#v}"
  local number='(0|[1-9][0-9]*)'
  local identifier='[0-9A-Za-z-]+'
  local pattern="^${number}\.${number}\.${number}(-${identifier}(\.${identifier})*)?(\+${identifier}(\.${identifier})*)?$"
  if [[ ! "$version" =~ $pattern ]]; then
    printf 'Invalid release version: %q\n' "$1" >&2
    return 1
  fi
  local core="${version%%+*}"
  if [[ "$core" == *-* ]]; then
    local prerelease="${core#*-}" part
    local -a parts
    IFS=. read -r -a parts <<< "$prerelease"
    for part in "${parts[@]}"; do
      if [[ "$part" =~ ^[0-9]+$ && "${#part}" -gt 1 && "$part" == 0* ]]; then
        printf 'Numeric prerelease identifiers cannot have leading zeros\n' >&2
        return 1
      fi
    done
  fi
  printf 'v%s\n' "$version"
}

verify_tag() {
  local tag="$1" normalized target
  normalized="$(resolve_version "$tag")"
  if [[ "$tag" != "$normalized" ]]; then
    printf 'Release tags must start with v\n' >&2
    return 1
  fi
  if git show-ref --verify --quiet "refs/tags/$tag"; then
    target="$(git rev-parse --verify "refs/tags/$tag^{commit}")"
    if [[ "$target" != "$(git rev-parse HEAD)" ]]; then
      printf 'Tag %s points to a different commit than the tested checkout\n' "$tag" >&2
      return 1
    fi
  fi
}

verify_source() {
  local tag="$1" root expected line found=false
  root="$(git rev-parse --show-toplevel)"
  expected="$(resolve_version "$(cat "$root/VERSION")")"
  if [[ "$tag" != "$expected" ]]; then
    printf 'Release %s does not match VERSION %s\n' "$tag" "$expected" >&2
    return 1
  fi
  while IFS= read -r line; do
    case "$line" in
      "## $tag"|"## $tag — "*) found=true ;;
    esac
  done < "$root/CHANGELOG.md"
  if [[ "$found" != true ]]; then
    printf 'CHANGELOG.md has no entry for %s\n' "$tag" >&2
    return 1
  fi
}

case "${1:-}" in
  resolve)
    [[ "$#" -eq 2 ]] || exit 2
    resolve_version "$2"
    ;;
  verify-source)
    [[ "$#" -eq 2 ]] || exit 2
    verify_source "$2"
    ;;
  verify-tag)
    [[ "$#" -eq 2 ]] || exit 2
    verify_tag "$2"
    ;;
  publish-tag)
    [[ "$#" -eq 2 ]] || exit 2
    verify_tag "$2"
    if ! git show-ref --verify --quiet "refs/tags/$2"; then
      git tag "$2" HEAD
    fi
    git push origin "refs/tags/$2"
    ;;
  *)
    printf 'Usage: release.sh {resolve|verify-source|verify-tag|publish-tag} VERSION\n' >&2
    exit 2
    ;;
esac
