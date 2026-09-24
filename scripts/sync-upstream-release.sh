#!/usr/bin/env bash
set -euo pipefail

branch=yeutech-capability-v15
upstream=https://github.com/router-for-me/CLIProxyAPI.git
current_branch=$(git branch --show-current)
if [[ "$current_branch" != "$branch" ]]; then
  echo "Expected $branch, got $current_branch" >&2
  exit 2
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo 'Working tree is not clean' >&2
  exit 2
fi

version=$(gh api repos/router-for-me/CLIProxyAPI/releases/latest --jq .tag_name)
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Unexpected upstream release tag: $version" >&2
  exit 2
fi
echo "Upstream release: $version"
git fetch --no-tags "$upstream" "refs/tags/$version:refs/tags/$version"
if git merge-base --is-ancestor "$version" HEAD; then
  echo "Already contains $version"
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then echo 'changed=false' >> "$GITHUB_OUTPUT"; fi
  exit 0
fi

git config user.name 'YEUTECH upstream sync'
git config user.email 'upstream-sync@users.noreply.github.com'
git config merge.yeutech-model-catalog.name 'Merge model catalog by model ID and field'
git config merge.yeutech-model-catalog.driver 'python3 scripts/merge_model_catalog.py %O %A %B'

if ! git merge --no-edit --no-ff "$version"; then
  printf 'Conflicting files:\n' >&2
  git diff --name-only --diff-filter=U >&2
  exit 1
fi
python3 -m unittest discover -s scripts -p test_merge_model_catalog.py
git diff "$version" HEAD -- internal/translator > /dev/null
echo "Merged $version into candidate $(git rev-parse --short HEAD); build and tests must pass before push"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then echo 'changed=true' >> "$GITHUB_OUTPUT"; fi
