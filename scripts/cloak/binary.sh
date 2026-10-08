#!/bin/sh
# Build/operator equivalent of the pinned wrapper's CLI; never used at runtime.
set -eu
command=${1:-info}
case "$command" in info|install|update|clear-cache) ;; *) echo "Usage: $0 [info|install|update|clear-cache] [operator-cache-directory]" >&2; exit 2;; esac
image=transactions-cloak-reference:inspect
if [ "$#" -ge 2 ]; then
 mkdir -p "$2"
 cache_dir=$(cd "$2" && pwd)
 docker run --rm --init --env CLOAKBROWSER_CACHE_DIR=/cache --mount "type=bind,src=$cache_dir,dst=/cache" --entrypoint node "$image" /tmp/browser-install/node_modules/cloakbrowser/dist/cli.js "$command"
else
 docker run --rm --init --entrypoint node "$image" /tmp/browser-install/node_modules/cloakbrowser/dist/cli.js "$command"
fi
