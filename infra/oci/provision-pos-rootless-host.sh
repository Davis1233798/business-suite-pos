#!/usr/bin/env bash
# Explicit, fail-closed one-time provisioning for the private POS OCI host.
set -Eeuo pipefail
umask 077

readonly STATE=/etc/pos-oci-provisioner
readonly MARKER=$STATE/owner
readonly ACCOUNT=pos
readonly HOME=/var/lib/pos
readonly DATA=/srv/business-suite-pos
readonly POS_DATA=/srv/business-suite-pos/data
readonly RELEASES=/srv/business-suite-pos/releases
readonly SHARED=/srv/business-suite-pos/shared
readonly LIB=/usr/local/lib/business-suite-pos
readonly GATEWAY=/usr/local/lib/business-suite-pos/ssh-gateway.sh
readonly DEPLOYER=/usr/local/lib/business-suite-pos/deploy-release.sh
readonly COMPOSE=/usr/local/lib/business-suite-pos/compose.yaml
readonly SSHD=/etc/ssh/sshd_config
readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
readonly REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd -P)"
MODE= KEY_FILE=

fail(){ printf 'ERROR: %s\n' "$*" >&2; exit 1; }
usage(){ echo "Usage: sudo bash $0 --check|--apply --deploy-public-key-file PATH"; }

while (($#)); do
  case "$1" in
    --check|--apply) [[ -z $MODE ]] || { usage >&2; exit 2; }; MODE=${1#--}; shift ;;
    --deploy-public-key-file) (($# >= 2)) || { usage >&2; exit 2; }; KEY_FILE=$2; shift 2 ;;
    *) usage >&2; exit 2 ;;
  esac
done
[[ $EUID == 0 ]] || fail 'must run as root'
[[ -n $MODE && -n $KEY_FILE && -f $KEY_FILE && ! -L $KEY_FILE && -r $KEY_FILE ]] || { usage >&2; exit 2; }
[[ -r /etc/os-release ]] || fail 'cannot identify host OS'
# shellcheck disable=SC1091
source /etc/os-release
[[ ${ID:-} == ubuntu && ${VERSION_ID:-} == 24.04 && $(dpkg --print-architecture) == arm64 && $(uname -m) == aarch64 ]] || fail 'requires Ubuntu 24.04 ARM64'
[[ -d /run/systemd/system && -r /sys/fs/cgroup/cgroup.controllers ]] || fail 'requires systemd with cgroup v2'
[[ $(wc -l < "$KEY_FILE") == 1 ]] || fail 'operator public key file must contain exactly one line'
read -r key_type key_blob _ < "$KEY_FILE"
[[ $key_type =~ ^(ssh-ed25519|sk-ssh-ed25519@openssh.com|ecdsa-sha2-nistp256|ssh-rsa)$ && $key_blob =~ ^[A-Za-z0-9+/]+=*$ ]] || fail 'invalid OpenSSH public key'
for cmd in /usr/bin/python3 /usr/bin/docker /usr/bin/systemctl /usr/bin/loginctl /usr/sbin/sshd /usr/bin/runuser /usr/bin/newuidmap /usr/bin/rootlesskit; do [[ -x $cmd ]] || fail "missing prerequisite $cmd"; done
command -v aa-status >/dev/null && aa-status --enabled || fail 'AppArmor must already be enabled'
[[ -f /etc/apparmor.d/rootlesskit ]] && aa-status | grep -Fq rootlesskit || fail 'the shared rootlesskit AppArmor profile must already be loaded'
[[ -d /usr/local/libexec/mes-rootless && ! -L /usr/local/libexec/mes-rootless ]] || fail 'pinned rootless launcher directory unavailable'
for launcher in /usr/local/libexec/mes-rootless/dockerd-rootless.sh /usr/local/libexec/mes-rootless/dockerd-rootless-setuptool.sh; do
  [[ -f $launcher && -x $launcher && $(stat -c '%u:%g:%a' "$launcher") == 0:0:755 ]] || fail 'pinned rootless launchers must be executable, immutable root-owned files'
done
[[ -f $SSHD && ! -L $SSHD && $(stat -c '%u:%g' "$SSHD") == 0:0 ]] || fail 'unsafe sshd_config'
sshd -t || fail 'existing SSH configuration invalid'

if [[ -e $STATE ]]; then
  [[ -d $STATE && ! -L $STATE && $(stat -c '%u:%g:%a' "$STATE") == 0:0:750 && -f $MARKER && ! -L $MARKER ]] || fail 'foreign or incomplete POS provisioner state'
  owner=$(cat "$MARKER")
  [[ $owner == business-suite-pos/host-provisioner/v1 || $owner == business-suite-pos/host-provisioner/v1-in-progress ]] || fail 'foreign or incomplete POS provisioner state'
  if [[ $owner == business-suite-pos/host-provisioner/v1 ]]; then
    [[ -d $LIB && ! -L $LIB && $(stat -c '%u:%g:%a' "$LIB") == 0:0:755 ]] || fail 'managed POS installation is incomplete'
    for file in deploy-release.sh ssh-gateway.sh compose.yaml; do cmp -s "$LIB/$file" "$REPO_ROOT/infra/oci/$file" || fail "installed POS program differs from source: $file"; done
    [[ -f $HOME/.ssh/authorized_keys && $(stat -c '%u:%g:%a' "$HOME/.ssh/authorized_keys") == pos:pos:600 ]] || fail 'POS deployment key installation is incomplete'
    grep -Fqx "restrict,command=\"$GATEWAY\",no-port-forwarding,no-agent-forwarding,no-pty $key_type $key_blob" "$HOME/.ssh/authorized_keys" || fail 'refusing to replace an existing POS deployment key'
    if [[ $MODE == apply ]]; then bash "$SCRIPT_DIR/verify-pos-host-isolation.sh"; echo 'POS host already provisioned; No changes made.'; exit 0; fi
  elif [[ $MODE == check ]]; then
    fail 'POS provisioner has recoverable partial state; rerun --apply with the same key'
  fi
else
  for path in "$HOME" "$DATA" "$LIB"; do [[ ! -e $path && ! -L $path ]] || fail "refusing to adopt $path"; done
  ! getent passwd "$ACCOUNT" && ! getent group "$ACCOUNT" || fail "refusing to adopt existing account/group $ACCOUNT"
  ! grep -q '^Match User pos$' "$SSHD" || fail 'existing POS SSH block found'
  ! grep -Eq '^pos:' /etc/subuid /etc/subgid 2>/dev/null || fail 'refusing to adopt existing POS subordinate ID mappings'
  for path in /etc/subuid /etc/subgid; do [[ ! -e $path ]] || { [[ -f $path && ! -L $path && $(stat -c '%u:%g' "$path") == 0:0 ]] || fail "unsafe $path"; }; done
fi

if [[ $MODE == check ]]; then
  [[ -d $STATE ]] || fail 'POS runtime is not provisioned'
  bash "$SCRIPT_DIR/verify-pos-host-isolation.sh"
  exit 0
fi

install -d -o root -g root -m 0750 "$STATE"
if [[ ! -e $MARKER ]]; then printf '%s\n' business-suite-pos/host-provisioner/v1-in-progress > "$MARKER"; chown root:root "$MARKER"; chmod 0444 "$MARKER"; fi
[[ -e /etc/subuid ]] || install -o root -g root -m 0644 /dev/null /etc/subuid
[[ -e /etc/subgid ]] || install -o root -g root -m 0644 /dev/null /etc/subgid
if getent passwd "$ACCOUNT" >/dev/null; then
  [[ $(getent passwd "$ACCOUNT" | cut -d: -f6) == "$HOME" && $(getent passwd "$ACCOUNT" | cut -d: -f7) == /bin/bash && $(id -u "$ACCOUNT") != 0 ]] || fail 'partial POS account does not match the expected dedicated account'
  [[ $(getent group "$ACCOUNT" | cut -d: -f3) == "$(id -g "$ACCOUNT")" ]] || fail 'partial POS account group is inconsistent'
  uid=$(id -u "$ACCOUNT")
else
  getent group "$ACCOUNT" >/dev/null && fail 'partial POS group exists without its account'
  uid=470
  while getent passwd "$uid" >/dev/null || getent group "$uid" >/dev/null; do uid=$((uid+1)); done
  (( uid < 60000 )) || fail 'no free POS account uid'
  groupadd --system --gid "$uid" "$ACCOUNT"
  useradd --system --uid "$uid" --gid "$ACCOUNT" --home-dir "$HOME" --create-home --shell /bin/bash "$ACCOUNT"
  passwd -l "$ACCOUNT" >/dev/null
fi
chmod 0700 "$HOME"
for db in /etc/subuid /etc/subgid; do
  count=$(grep -c "^$ACCOUNT:" "$db" || true)
  if (( count == 0 )); then
    sub=${sub:-500000}
    while awk -F: -v s="$sub" -v e="$((sub+65535))" '$2<=e && $2+$3-1>=s {found=1} END{exit !found}' /etc/subuid /etc/subgid 2>/dev/null; do sub=$((sub+65536)); done
    (( sub+65535 < 4294967295 )) || fail 'no free POS subordinate ID range'
    printf '%s:%s:65536\n' "$ACCOUNT" "$sub" >> "$db"
  elif (( count != 1 )) || [[ $(grep "^$ACCOUNT:" "$db" | cut -d: -f3) != 65536 ]]; then
    fail "partial POS subordinate ID mapping is inconsistent in $db"
  fi
done
chown root:root /etc/subuid /etc/subgid; chmod 0644 /etc/subuid /etc/subgid
if [[ -e $LIB || -L $LIB ]]; then
  [[ -d $LIB && ! -L $LIB && $(stat -c '%u:%g:%a' "$LIB") == 0:0:755 ]] || fail 'refusing to adopt unsafe POS program directory'
else
  install -d -o root -g root -m 0755 "$LIB"
fi
install -o root -g root -m 0555 "$REPO_ROOT/infra/oci/deploy-release.sh" "$DEPLOYER"
install -o root -g root -m 0555 "$REPO_ROOT/infra/oci/ssh-gateway.sh" "$GATEWAY"
install -o root -g root -m 0444 "$REPO_ROOT/infra/oci/compose.yaml" "$COMPOSE"
for path in "$DATA" "$POS_DATA" "$RELEASES" "$SHARED"; do
  if [[ -e $path || -L $path ]]; then
    [[ -d $path && ! -L $path && $(stat -c '%U:%a' "$path") == "$ACCOUNT:700" ]] || fail "refusing to adopt unsafe POS data path $path"
  else
    install -d -o "$ACCOUNT" -g "$ACCOUNT" -m 0700 "$path"
  fi
done
install -d -o "$ACCOUNT" -g "$ACCOUNT" -m 0700 "$HOME/.ssh"
key_line="restrict,command=\"$GATEWAY\",no-port-forwarding,no-agent-forwarding,no-pty $key_type $key_blob"
if [[ -e $HOME/.ssh/authorized_keys ]]; then
  [[ -f $HOME/.ssh/authorized_keys && ! -L $HOME/.ssh/authorized_keys && $(stat -c '%U:%a' "$HOME/.ssh/authorized_keys") == "$ACCOUNT:600" && $(cat "$HOME/.ssh/authorized_keys") == "$key_line" ]] || fail 'refusing to replace an existing POS deployment key'
else
  printf '%s\n' "$key_line" > "$HOME/.ssh/authorized_keys"
fi
chown -R "$ACCOUNT:$ACCOUNT" "$HOME/.ssh"; chmod 0700 "$HOME/.ssh"; chmod 0600 "$HOME/.ssh/authorized_keys"
if [[ ! -e $STATE/sshd_config.before ]]; then cp -a "$SSHD" "$STATE/sshd_config.before"; chmod 0600 "$STATE/sshd_config.before"; fi
loginctl enable-linger "$ACCOUNT"
runtime_uid=$(id -u "$ACCOUNT")
for _ in $(seq 1 30); do [[ -S /run/user/$runtime_uid/bus ]] && break; sleep 1; done
[[ -S /run/user/$runtime_uid/bus ]] || fail 'POS user bus unavailable'
runuser -u "$ACCOUNT" -- env -i HOME="$HOME" PATH=/usr/bin:/bin install -d -m 0700 "$HOME/.config/systemd/user/docker.service.d"
runuser -u "$ACCOUNT" -- env -i HOME="$HOME" PATH=/usr/bin:/bin sh -c 'umask 077; cat > "$HOME/.config/systemd/user/docker.service.d/pos-environment.conf"' <<EOF
[Service]
Environment=HOME=$HOME
Environment=USER=$ACCOUNT
Environment=LOGNAME=$ACCOUNT
Environment=XDG_RUNTIME_DIR=/run/user/$runtime_uid
Environment=XDG_DATA_HOME=$HOME/.local/share
Environment=DOCKER_CONFIG=/var/lib/pos/.docker
UnsetEnvironment=DOCKER_HOST DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH DOCKER_API_VERSION
ExecStartPost=/usr/bin/chmod 0600 /run/user/$runtime_uid/docker.sock
EOF
runuser -u "$ACCOUNT" -- env -i HOME="$HOME" USER="$ACCOUNT" LOGNAME="$ACCOUNT" XDG_RUNTIME_DIR="/run/user/$runtime_uid" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$runtime_uid/bus" DOCKER_CONFIG="$HOME/.docker" PATH=/usr/local/libexec/mes-rootless:/usr/bin:/bin /usr/local/libexec/mes-rootless/dockerd-rootless-setuptool.sh install --force
for _ in $(seq 1 60); do [[ -S /run/user/$runtime_uid/docker.sock ]] && break; sleep 2; done
[[ -S /run/user/$runtime_uid/docker.sock && $(stat -c '%U:%a' "/run/user/$runtime_uid/docker.sock") == "$ACCOUNT:600" ]] || fail 'rootless Docker socket unavailable or unsafe'
[[ $(systemctl is-active docker.service) == active ]] || fail 'rootful Docker service was disrupted'
if ! grep -q '^Match User pos$' "$SSHD"; then
  cat >> "$SSHD" <<EOF

Match User pos
  ForceCommand /usr/local/lib/business-suite-pos/ssh-gateway.sh
  AuthenticationMethods publickey
  PasswordAuthentication no
  KbdInteractiveAuthentication no
  PermitTTY no
  DisableForwarding yes
  PermitUserRC no
EOF
fi
if ! sshd -t; then cp -a "$STATE/sshd_config.before" "$SSHD"; fail 'sshd rejected POS policy; original config restored'; fi
effective=$(sshd -T -C "user=pos,host=$(hostname -f),addr=127.0.0.1")
if ! grep -Fq "forcecommand $GATEWAY" <<< "$effective" || ! grep -Fq 'authenticationmethods publickey' <<< "$effective" || ! grep -Fq 'disableforwarding yes' <<< "$effective"; then
  cp -a "$STATE/sshd_config.before" "$SSHD"
  fail 'effective SSH policy is not limited to the POS forced command; original config restored'
fi
if systemctl is-active --quiet ssh.service && ! systemctl reload ssh.service; then cp -a "$STATE/sshd_config.before" "$SSHD"; systemctl reload ssh.service || true; fail 'SSH daemon did not reload POS policy; original config restored'; fi
printf '%s\n' business-suite-pos/host-provisioner/v1 > "$MARKER"; chown root:root "$MARKER"; chmod 0444 "$MARKER"
bash "$SCRIPT_DIR/verify-pos-host-isolation.sh"
echo 'POS rootless host provisioned. No POS container was deployed.'
