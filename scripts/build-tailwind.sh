#!/usr/bin/env bash
# build-tailwind.sh — compile Tailwind CSS to a static file for production
#
# Replaces the Tailwind Play CDN (which violates script-src 'self' CSP via
# blob: web workers) with a pre-built stylesheet.
#
# Run from the repo root:
#   bash scripts/build-tailwind.sh
#
# Output: app/tailwind.css  — minified production stylesheet
#
# Requires: Node.js + npx (tailwindcss is installed automatically via npx)

set -euo pipefail

OUT="app/tailwind.css"

# Install tailwindcss v3 locally if not already present (v4 dropped the bundled CLI)
if [ ! -f node_modules/.bin/tailwindcss ]; then
  echo "→ Installing tailwindcss v3..."
  npm install --save-dev tailwindcss@3
fi

echo "→ Building Tailwind CSS..."
./node_modules/.bin/tailwindcss \
  --config tailwind/tailwind.config.js \
  --input  tailwind/tailwind.input.css \
  --output "$OUT" \
  --minify

echo "→ Done: $OUT ($(wc -c < "$OUT" | tr -d ' ') bytes)"
echo ""
echo "Deploy $OUT alongside index.html."
echo "The <link rel=\"stylesheet\" href=\"/tailwind.css\"> in index.html serves it."
