#!/usr/bin/env bash
# update-vendor-js.sh — (re)generate the vendored frontend JS libraries used
# by Phase 5d's rich-text comments: a Markdown renderer and an HTML
# sanitizer. These are served locally (app/vendor/*.min.js) rather than
# pulled from a CDN, matching this app's existing self-hosted-assets
# convention (alpinejs.min.js, tailwind.css, fonts are all vendored the
# same way — see scripts/build-tailwind.sh for the same pattern).
#
# Run this:
#   - once, right after cloning, so app/vendor/ is populated
#   - again any time you want to bump marked/DOMPurify to a newer version
#   - as a release-testing sanity check, to confirm the two files this app
#     depends on actually exist where photo.html expects them
#       (app/vendor/marked.min.js, app/vendor/purify.min.js)
#
# Run from the repo root:
#   bash scripts/update-vendor-js.sh
#
# Output:
#   app/vendor/marked.min.js   — Markdown → HTML renderer
#   app/vendor/purify.min.js   — HTML sanitizer (strips anything unsafe
#                                 before rendered comments are ever shown)
#
# Requires: Node.js + npm. Installs into this repo's own node_modules
# (same as build-tailwind.sh), not a temp directory, so repeat runs are fast.

set -euo pipefail

OUT_DIR="app/vendor"
mkdir -p "$OUT_DIR"

echo "→ Installing marked, dompurify, terser..."
npm install --no-save marked dompurify terser

echo "→ Minifying marked (its npm package ships an unminified UMD build)..."
./node_modules/.bin/terser \
  node_modules/marked/lib/marked.umd.js \
  -c -m -o "$OUT_DIR/marked.min.js"

echo "→ Copying DOMPurify's pre-minified UMD build..."
cp node_modules/dompurify/dist/purify.min.js "$OUT_DIR/purify.min.js"

echo ""
echo "→ Done:"
echo "   $OUT_DIR/marked.min.js  ($(wc -c < "$OUT_DIR/marked.min.js" | tr -d ' ') bytes)"
echo "   $OUT_DIR/purify.min.js  ($(wc -c < "$OUT_DIR/purify.min.js" | tr -d ' ') bytes)"
echo ""
echo "photo.html loads these via:"
echo '  <script src="/vendor/marked.min.js"></script>'
echo '  <script src="/vendor/purify.min.js"></script>'
echo "Confirm they're reachable at those URLs before release testing —"
echo "if either 404s, the Markdown comment renderer silently fails (every"
echo "call to marked.parse()/DOMPurify.sanitize() throws ReferenceError)."
