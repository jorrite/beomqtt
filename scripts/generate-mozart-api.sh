#!/usr/bin/env bash
# Regenerate internal/mozartapi from the vendored Mozart OpenAPI spec.
#
# The spec lives in api/ (vendored from
# https://github.com/bang-olufsen/mozart-open-api). To update: replace the
# yaml in api/, adjust SPEC below, and re-run this script (via `just
# generate`). Requires Docker.
set -euo pipefail
cd "$(dirname "$0")/.."

SPEC="api/mozart-api-6.2.0.44.yaml"
GENERATOR_IMAGE="openapitools/openapi-generator-cli:v7.10.0"
OUT="internal/mozartapi"

# Every operation in the spec carries a redundant catch-all "mozart" tag next
# to its category tag, and one operation (start-deezer-flow) has two category
# tags. openapi-generator's go target emits one API file per tag, so any
# multi-tagged operation is generated twice and the package fails to compile
# with redeclaration errors. Keep only the first tag per operation.
tmp_spec="api/.generation-spec.yaml"
trap 'rm -f "$tmp_spec"' EXIT
awk '
  /^      tags:$/ { print; intags=1; kept=0; next }
  intags && /^        - / { if (!kept) { print; kept=1 }; next }
  { intags=0; print }
' "$SPEC" > "$tmp_spec"

rm -rf "$OUT"
docker run --rm -v "$PWD":/local "$GENERATOR_IMAGE" generate \
    -i "/local/$tmp_spec" \
    -g go \
    -o "/local/$OUT" \
    --package-name mozartapi \
    --additional-properties=withGoMod=false,enumClassPrefix=true \
    --global-property=apiTests=false,modelTests=false,apiDocs=false,modelDocs=false

# Repo-level cruft the generator writes that has no place in an internal
# vendored package.
rm -f "$OUT/git_push.sh" "$OUT/.travis.yml" "$OUT/.gitignore" "$OUT/.openapi-generator-ignore"

gofmt -w "$OUT"
go build ./...
echo "OK: regenerated $OUT from $SPEC"
