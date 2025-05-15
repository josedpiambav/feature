#!/bin/sh
set -e

git config --global --add safe.directory /github/workspace
git config --global user.name "GitHub Actions"
git config --global user.email "41898282+github-actions[bot]@users.noreply.github.com"
git config --global advice.addIgnoredFile false

/usr/local/bin/feature-branching \
  --github_token "${INPUT_GITHUB_TOKEN}" \
  ${INPUT_OWNER:+--owner "${INPUT_OWNER}"} \
  ${INPUT_REPO:+--repo "${INPUT_REPO}"} \
  ${INPUT_TRUNK_BRANCH:+--trunk_branch "${INPUT_TRUNK_BRANCH}"} \
  ${INPUT_TARGET_BRANCH:+--target_branch "${INPUT_TARGET_BRANCH}"} \
  ${INPUT_LABELS:+--labels "${INPUT_LABELS}"}