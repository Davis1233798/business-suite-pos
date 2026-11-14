#!/usr/bin/env bash
set -Eeuo pipefail

readonly deployer=/usr/local/lib/business-suite-pos/deploy-release.sh
readonly compose=/usr/local/lib/business-suite-pos/compose.yaml
command_line=${SSH_ORIGINAL_COMMAND:-}

if [[ $command_line =~ ^deploy\ ([0-9a-f]{40})\ (sha256:[0-9a-f]{64})$ ]]; then
  sha=${BASH_REMATCH[1]}
  digest=${BASH_REMATCH[2]}
  [[ -x $deployer && -f $compose ]] || {
    echo 'POS deployment runtime is not provisioned' >&2
    exit 3
  }
  exec "$deployer" "$sha" "$digest"
fi

echo 'This SSH key may only request a POS release by commit and image digest' >&2
exit 2
