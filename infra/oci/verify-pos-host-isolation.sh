#!/usr/bin/env bash
set -Eeuo pipefail
fail(){ echo "FAIL: $*" >&2; exit 1; }
getent passwd pos >/dev/null || fail 'missing pos account'
uid=$(id -u pos)
[[ $uid != 0 && " $(id -nG pos) " != *' docker '* ]] || fail 'POS account is root or joined rootful docker group'
[[ $(getent passwd pos | cut -d: -f6) == /var/lib/pos ]] || fail 'POS home directory mismatch'
[[ $(stat -c '%U:%a' /var/lib/pos) == pos:700 ]] || fail 'POS home permissions invalid'
for path in /srv/business-suite-pos /srv/business-suite-pos/data /srv/business-suite-pos/releases /srv/business-suite-pos/shared; do
  [[ -d $path && ! -L $path && $(stat -c '%U:%a' "$path") == pos:700 ]] || fail "POS data path ownership/mode invalid: $path"
done
for file in /usr/local/lib/business-suite-pos/deploy-release.sh /usr/local/lib/business-suite-pos/ssh-gateway.sh; do [[ $(stat -c '%U:%a' "$file") == root:555 ]] || fail "installed POS executable is not immutable root-owned: $file"; done
[[ $(stat -c '%U:%a' /usr/local/lib/business-suite-pos/compose.yaml) == root:444 ]] || fail 'installed POS Compose file is not immutable root-owned'
[[ $(stat -c '%U:%a' /usr/local/lib/business-suite-pos) == root:755 ]] || fail 'POS program directory ownership/mode invalid'
[[ $(stat -c '%U:%a' /var/lib/pos/.ssh/authorized_keys) == pos:600 ]] || fail 'POS authorized_keys ownership/mode invalid'
[[ $(wc -l < /var/lib/pos/.ssh/authorized_keys) == 1 && $(cut -d, -f1 /var/lib/pos/.ssh/authorized_keys) == restrict ]] || fail 'POS deployment key is not restricted'
grep -Fq 'no-port-forwarding,no-agent-forwarding,no-pty' /var/lib/pos/.ssh/authorized_keys || fail 'POS key forwarding and tty restrictions are incomplete'
runtime_dir=/run/user/$uid
[[ -S $runtime_dir/docker.sock && $(stat -c '%U:%a' "$runtime_dir/docker.sock") == pos:600 ]] || fail 'rootless Docker socket ownership/mode invalid'
security=$(runuser -u pos -- env -i HOME=/var/lib/pos DOCKER_CONFIG=/var/lib/pos/.docker XDG_RUNTIME_DIR="$runtime_dir" DOCKER_HOST="unix://$runtime_dir/docker.sock" docker info --format '{{json .SecurityOptions}}')
[[ $security == *'"name=rootless"'* ]] || fail 'Docker daemon is not rootless'
root=$(runuser -u pos -- env -i HOME=/var/lib/pos DOCKER_CONFIG=/var/lib/pos/.docker XDG_RUNTIME_DIR="$runtime_dir" DOCKER_HOST="unix://$runtime_dir/docker.sock" docker info --format '{{.DockerRootDir}}')
[[ $root == /var/lib/pos/* ]] || fail 'Docker data escaped POS runtime home'
effective=$(sshd -T -C "user=pos,host=$(hostname -f),addr=127.0.0.1")
grep -Fq 'forcecommand /usr/local/lib/business-suite-pos/ssh-gateway.sh' <<< "$effective" || fail 'SSH command is not forced to POS gateway'
grep -Fq 'authenticationmethods publickey' <<< "$effective" || fail 'SSH does not require public key'
grep -Fq 'disableforwarding yes' <<< "$effective" || fail 'SSH forwarding is not disabled'
[[ $(systemctl is-active docker.service) == active ]] || fail 'rootful Docker is not active'
aa-status --enabled || fail 'AppArmor is not enabled'
echo 'PASS: POS deployment uses isolated rootless Docker and a digest-only SSH gateway.'
