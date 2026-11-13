#!/usr/bin/env bash
set -Eeuo pipefail

readonly root=/srv/business-suite-pos
readonly deployer="$root/shared/deploy-release.sh"
command_line=${SSH_ORIGINAL_COMMAND:-}

if [[ $command_line =~ ^deploy\ ([0-9a-f]{40})\ (sha256:[0-9a-f]{64})$ ]]; then
  sha=${BASH_REMATCH[1]}
  digest=${BASH_REMATCH[2]}
  [[ -x $deployer && -f $root/shared/compose.yaml ]] || {
    echo 'POS deployment runtime is not provisioned' >&2
    exit 3
  }
  exec "$deployer" "$sha" "$digest"
fi

echo 'This SSH key may only request a POS release by commit and image digest' >&2
exit 2
