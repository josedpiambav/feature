#!/bin/sh
set -e

/usr/local/bin/feature-branching \
  --github_token "${INPUT_GITHUB_TOKEN}" \
  --owner "${INPUT_OWNER}" \
  --repo "${INPUT_REPO}" \
  --trunk_branch "${INPUT_TRUNK_BRANCH}" \
  --target_branch "${INPUT_TARGET_BRANCH}" \
  --merge_strategy "${INPUT_MERGE_STRATEGY}" \
  --labels "${INPUT_LABELS}"