#!/usr/bin/env bash
#
# release.sh — cut the next release: bump the version from the newest vX.Y.Z tag, write it where
# the code carries it (the chart's version and appVersion), commit "Release vX.Y.Z", tag it, push
# both, and publish the GitHub Release (notes generated from the PRs since the last one; needs
# gh). The Release workflow (.github/workflows/release.yml) runs it on main; it also runs locally.
#
#   scripts/release.sh [patch|minor|major] [--dry-run] [--no-publish]
#
# --dry-run prints the next version and the changes, and touches nothing. --no-publish stops after
# the push: the workflow publishes the Release once the release's images are up. Under Actions the
# new tag and the one before it are written to $GITHUB_OUTPUT (tag, last).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART="${ROOT}/deploy/helm/aintercept/Chart.yaml"
BUMP=patch
DRY=false
PUBLISH=true
for a in "$@"; do
  case "$a" in
    patch|minor|major) BUMP="$a" ;;
    --dry-run) DRY=true ;;
    --no-publish) PUBLISH=false ;;
    *) echo "usage: $0 [patch|minor|major] [--dry-run] [--no-publish]" >&2; exit 2 ;;
  esac
done

cd "$ROOT"
git fetch --quiet --tags origin
last="$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -1)"
[[ -n "$last" ]] || { echo "no vX.Y.Z tag to bump from" >&2; exit 1; }
IFS=. read -r major minor patch <<< "${last#v}"
case "$BUMP" in
  major) next="$((major + 1)).0.0" ;;
  minor) next="${major}.$((minor + 1)).0" ;;
  patch) next="${major}.${minor}.$((patch + 1))" ;;
esac
tag="v${next}"
git rev-parse --quiet --verify "refs/tags/${tag}" >/dev/null && { echo "${tag} already exists" >&2; exit 1; }

echo "${last} -> ${tag} (${BUMP})"
if $DRY; then
  sed -n -e 's/^version: .*/version: '"${next}"'/p' -e 's/^appVersion: .*/appVersion: "'"${next}"'"/p' "$CHART"
  exit 0
fi

[[ -z "$(git status --porcelain)" ]] || { echo "the working tree isn't clean" >&2; exit 1; }
sed -i.bak -e 's/^version: .*/version: '"${next}"'/' -e 's/^appVersion: .*/appVersion: "'"${next}"'"/' "$CHART"
rm -f "${CHART}.bak"
grep -q "^version: ${next}$" "$CHART" && grep -q "^appVersion: \"${next}\"$" "$CHART" \
  || { echo "couldn't write ${next} into ${CHART}" >&2; exit 1; }

git add "$CHART"
git commit --quiet -m "Release ${tag}"
git tag -a "$tag" -m "Release ${tag}"
git push --quiet origin HEAD "$tag"
[[ -z "${GITHUB_OUTPUT:-}" ]] || printf 'tag=%s\nlast=%s\n' "$tag" "$last" >> "$GITHUB_OUTPUT"
if $PUBLISH; then
  gh release create "$tag" --verify-tag --title "$tag" --generate-notes --notes-start-tag "$last" --latest
fi
echo "released ${tag} at $(git rev-parse --short HEAD)"
