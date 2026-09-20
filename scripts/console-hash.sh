#!/bin/sh
# Prints one hash over every input of the console build. `npm run build`
# writes it to cmd/parley/dist/SOURCE; `make check` and CI recompute it, so
# a change to console/ without a rebuilt, committed dist fails the check
# (the binary embeds dist, not the sources).
set -e
cd "$(dirname "$0")/../console"
find src index.html package.json package-lock.json vite.config.js tailwind.config.js postcss.config.js -type f \
  | LC_ALL=C sort | xargs shasum -a 256 | shasum -a 256 | cut -d' ' -f1
