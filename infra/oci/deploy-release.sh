#!/usr/bin/env bash
set -Eeuo pipefail

sha=${1:?commit SHA required}
digest=${2:?registry manifest digest required}
[[ $sha =~ ^[0-9a-f]{40}$ ]] || { echo 'Invalid commit SHA' >&2; exit 2; }
[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || { echo 'Invalid image digest' >&2; exit 2; }

readonly root=/srv/business-suite-pos
readonly repository=ghcr.io/davis1233798/business-suite-pos-station
readonly image="$repository@$digest"
readonly release="$root/releases/$sha"
readonly release_tmp="$root/releases/.${sha}.$$"
readonly shared_compose="$root/shared/compose.yaml"
readonly data_dir="$root/data"

[[ $(id -un) == pos ]] || { echo 'Deployment must run as the dedicated pos account' >&2; exit 3; }
if [[ -z ${XDG_RUNTIME_DIR:-} ]]; then
  export XDG_RUNTIME_DIR="/run/user/$(id -u)"
fi
[[ -S "$XDG_RUNTIME_DIR/docker.sock" ]] || {
  echo 'Rootless Docker socket is unavailable for the pos account' >&2
  exit 3
}
export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/docker.sock"

[[ ! -L "$root" && ! -L "$root/data" && ! -L "$root/releases" ]] || {
  echo 'POS deployment paths must not be symbolic links' >&2
  exit 3
}
[[ -s "$shared_compose" && ! -L "$shared_compose" ]] || {
  echo 'POS runtime Compose configuration is not provisioned' >&2
  exit 3
}
install -d -m 700 "$root" "$root/releases" "$root/shared" "$data_dir"
exec 9>"$root/shared/deploy.lock"
flock -n 9 || { echo 'Another POS deployment is running' >&2; exit 4; }

cleanup() {
  rm -f -- "$root/.current.$$"
  rm -rf -- "$release_tmp"
}
trap cleanup EXIT

previous=''
if [[ -L "$root/current" ]]; then
  previous=$(readlink -f "$root/current") || {
    echo 'Current POS release pointer cannot be resolved' >&2
    exit 5
  }
  [[ -f $previous/compose.yaml && -f $previous/COMMIT && -f $previous/IMAGE ]] || {
    echo 'Current POS release pointer does not resolve to a complete release' >&2
    exit 5
  }
elif [[ -e "$root/current" ]]; then
  echo 'Current POS release pointer must be a symbolic link' >&2
  exit 5
fi

if [[ -e $release ]]; then
  [[ -d $release && ! -L $release && -f $release/COMMIT && -f $release/IMAGE && -f $release/compose.yaml ]] || {
    echo 'Existing POS release is incomplete' >&2
    exit 5
  }
  [[ $(<"$release/COMMIT") == "$sha" && $(<"$release/IMAGE") == "$image" ]] || {
    echo 'Commit already has a different immutable image digest' >&2
    exit 5
  }
else
  mkdir -m 700 "$release_tmp"
  install -m 600 "$shared_compose" "$release_tmp/compose.yaml"
  cat > "$release_tmp/release.env" <<EOF
POS_IMAGE=$image
POS_REVISION=$sha
POS_DATA_DIR=$data_dir
EOF
  chmod 600 "$release_tmp/release.env"
  printf '%s\n' "$sha" > "$release_tmp/COMMIT"
  printf '%s\n' "$image" > "$release_tmp/IMAGE"
  chmod 600 "$release_tmp/COMMIT" "$release_tmp/IMAGE"
  mv "$release_tmp" "$release"
fi

compose() {
  local dir=$1
  shift
  docker compose --project-name pos-oci --project-directory "$dir" \
    --env-file "$dir/release.env" -f "$dir/compose.yaml" "$@"
}

check_health() {
  local expected=$1
  curl --fail --silent --show-error --retry 5 --retry-delay 2 \
    http://127.0.0.1:8095/api/health | python3 -c \
    'import json,sys; expected=sys.argv[1]; actual=json.load(sys.stdin)["data"]["revision"]; assert actual == expected, f"revision mismatch: {actual!r}"' "$expected"
}

rollback() {
  local status=$?
  trap - ERR
  echo "POS release failed ($status); restoring the previous digest" >&2
  local rollback_failed=0
  if [[ -n $previous ]]; then
    if ! compose "$previous" up -d --no-build --pull never --wait --wait-timeout 180 station; then
      rollback_failed=1
    elif ! check_health "$(<"$previous/COMMIT")"; then
      rollback_failed=1
    fi
  elif ! compose "$release" down --remove-orphans; then
    rollback_failed=1
  fi
  if (( rollback_failed )); then
    echo 'POS rollback failed; prior service health was not restored' >&2
    exit 70
  fi
  exit "$status"
}

docker pull "$image"
architecture=$(docker image inspect "$image" --format '{{.Architecture}}')
[[ $architecture == arm64 ]] || { echo "Expected arm64 image, got $architecture" >&2; exit 6; }
repo_digests=$(docker image inspect "$image" --format '{{range .RepoDigests}}{{println .}}{{end}}')
printf '%s\n' "$repo_digests" | grep -Fqx "$image" || {
  echo 'Pulled image does not report the requested repository digest' >&2
  exit 6
}
compose "$release" config --quiet
trap rollback ERR
compose "$release" up -d --no-build --pull never --wait --wait-timeout 180 station
check_health "$sha"

ln -s "$release" "$root/.current.$$"
mv -Tf "$root/.current.$$" "$root/current"
trap - ERR
printf 'Deployed POS commit %s with %s to private loopback\n' "$sha" "$digest"
