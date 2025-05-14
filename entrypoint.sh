#!/bin/sh
set -e

/usr/local/bin/feature-branching \
  --github_token "$INPUT_GITHUB-TOKEN" \
  --owner "$INPUT_OWNER" \
  --repo "$INPUT_REPO" \
  --trunk_branch "$INPUT_TRUNK-BRANCH" \
  --target_branch "$INPUT_TARGET-BRANCH" \
  --merge_strategy "$INPUT_MERGE-STRATEGY" \
  --labels "$INPUT_LABELS"