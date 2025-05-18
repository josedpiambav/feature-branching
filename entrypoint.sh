#!/bin/sh
set -e

/usr/local/bin/feature-branching \
  --github_token "${INPUT_GITHUB_TOKEN}" \
  --owner "${INPUT_OWNER}" \
  --repo "${INPUT_REPO}" \
  ${INPUT_TRUNK_BRANCH:+--trunk_branch "${INPUT_TRUNK_BRANCH}"} \
  ${INPUT_TARGET_BRANCH:+--target_branch "${INPUT_TARGET_BRANCH}"} \
  ${INPUT_LABELS:+--labels "${INPUT_LABELS}"} \
  --github_output "$GITHUB_OUTPUT"
