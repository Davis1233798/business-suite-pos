# Private OCI image release and deployment

POS is delivered as an immutable GHCR image for each main revision. CI builds and validates native AMD64 and ARM64 images. The OCI host runs the ARM64 image by its registry digest, never by a moving tag. Publication and runtime deployment are separate stages; pull requests validate only. The OCI deploy job also requires repository variable `POS_DEPLOY_ENABLED=true`, so validation and image publication can operate while the host account and pinned SSH secrets are being provisioned.

## Architecture and boundaries

- Runtime is a dedicated unprivileged `pos` Linux account using rootless Docker Compose. It does not use the shared Business Suite Compose project, root Docker socket, or shared service account.
- `/srv/business-suite-pos` owns only POS release files, the rootless daemon configuration, and POS data. No other system directory, environment file, Docker project, network, or data volume is read or modified.
- The POS container binds only to `127.0.0.1:8095`. An operator may connect through an existing private VPN or SSH tunnel. This release does not configure a public proxy, DNS, TLS endpoint, firewall rule, or external route.
- `POS_IMAGE` is pinned to `ghcr.io/...@sha256:<digest>`. Deploy and rollback both retain the exact image reference and release configuration. Data persists in an isolated POS-only host directory and is never deleted by rollback.
- CI connects with a POS-only SSH key and a separately pinned OCI host key. The GHCR image is public and requires no registry credential. The host-side deployment script needs no sudo and accepts only a validated commit SHA and image digest.

## Release flow

Pull requests run application tests, build both native images, and smoke-test each image without publishing or connecting to OCI. A push to `main` publishes architecture-suffixed commit tags, resolves the ARM64 registry manifest digest, and starts the private deployment job. The deployment job verifies that the commit remains the latest `main` revision before sending only the SHA and digest over SSH. GitHub Actions concurrency serializes releases.

The SSH key is restricted by `authorized_keys` to `infra/oci/ssh-gateway.sh`; the gateway accepts only a validated `deploy <commit-sha> <sha256-digest>` request and invokes host-installed POS deployment files. The CI key cannot upload scripts or Compose files, request a shell, or forward ports. The host creates an immutable release directory, validates the image architecture and digest, starts the release on loopback, and checks `/api/health` for the expected revision. Only after health passes does it atomically update the `current` pointer. A failed deployment restores the prior release using its recorded digest and verifies prior health. A failed rollback is surfaced in the SSH job log; it does not alter persistent POS data.

## Required GitHub secrets

- `OCI_POS_DEPLOY_KEY`: key authorized for the dedicated unprivileged POS deployment account.
- `OCI_POS_HOST_KEY`: pre-verified SSH host key line for the target host.
- `OCI_POS_HOST`: SSH destination for the dedicated POS deployment account, such as `pos@<host>`.

Set `POS_DEPLOY_ENABLED=true` only after one-time host setup is complete and all three repository secrets are present. Leaving it unset keeps OCI deployment disabled while validation and image publication remain active.

Do not reuse the suite deployment key or suite environment file. Repository/workflow administrators can read or change deployment configuration, so access to these secrets grants POS deployment ability.

## One-time OCI account setup

Provision a dedicated `pos` account with rootless Docker Compose and a running user-level Docker service. Install `ssh-gateway.sh` as `/srv/business-suite-pos/shared/ssh-gateway.sh`, `deploy-release.sh` as `/srv/business-suite-pos/shared/deploy-release.sh`, and `compose.yaml` as `/srv/business-suite-pos/shared/compose.yaml`, all owned by `pos` and mode `0700` (Compose may be `0600`). Create `data`, `releases`, and `shared` as `pos`-owned mode `0700` directories. Do not install into or reuse the shared suite tree.

Add the dedicated public key to the POS account's `~/.ssh/authorized_keys` with this restriction (replace the placeholder with the public key from `OCI_POS_DEPLOY_KEY`):

```text
command="/srv/business-suite-pos/shared/ssh-gateway.sh",no-agent-forwarding,no-port-forwarding,no-X11-forwarding,no-pty ssh-ed25519 AAAA... POS-GITHUB-ACTIONS
```

The account needs a normal shell for the forced command to run, but this key cannot request that shell. Update the host-installed gateway, deploy script, or Compose file through a separately controlled POS host administration path when their logic changes; the CI key can only request releases by digest.

## Validation and operational caveats

The health revision and architecture are checked before activation. Rollback restores application image and configuration, not database schema changes. Changes that are not backward compatible with existing POS data require a separate migration and recovery plan. A successful private loopback health check proves the container deployment only; private VPN/tunnel access must be validated separately. No deployment is considered public or externally reachable from this workflow.
