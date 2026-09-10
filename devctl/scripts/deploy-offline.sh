#!/bin/sh
# Run after SSH login. JSON carries all site-specific choices.
set -eu
if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo 'usage: deploy-offline.sh install.json [discover|plan|apply|verify|export]' >&2
  exit 2
fi
operation=${2:-apply}
case "$operation" in discover|plan|apply|verify|export) ;; *) exit 2 ;; esac
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo 'unsupported architecture' >&2; exit 2 ;; esac
exec "$script_dir/../bin/linux-$arch/devctl" admin "$operation" --config "$1" --format json
