# POS OCI deployment boundaries

## Architecture

The POS repository owns its image, OCI deployment workflow, rootless runtime project, and POS-only persistent data path. Its release is independent of the portfolio, ERP, and shared Business Suite Compose project. CI must pin the deployed ARM64 manifest digest and the deployment identity must be limited to the dedicated POS account.

## Data and credential boundary

The host account, `/srv/business-suite-pos` tree, rootless Docker context, Compose project name, host port, and application data directory are POS-specific. The deployment must not use `sudo`, access the root Docker socket, copy an environment file from another system, or remove persistent data. The public GHCR image does not need a registry credential. The CI SSH key is restricted to one forced-command gateway that accepts only a commit SHA and image digest; it cannot stage arbitrary code or request an interactive shell. Workflow secrets carry the POS SSH key, pinned host key, and SSH destination; these are separate from the other suite's deployment credentials.

## Network boundary

The service binds to host loopback port 8095 only. The workflow does not change OCI ingress, public DNS, Caddy, TLS, or any shared routing. Private reachability can be provided separately through an already authorized VPN or SSH tunnel.

## Release and rollback boundary

Each release records commit, complete image digest, and its Compose configuration in an immutable directory. Health acceptance requires the live image digest and application revision to match the release. The current release pointer changes only after health passes. Failure restores the preceding release from its recorded digest and keeps POS data untouched. Database schema reversibility is outside the image rollback guarantee.

## Acceptance sequence

1. Validate the architecture and isolation design in `OCI_IMAGE_RELEASE.md`.
2. Add regression checks for commit/digest validation, rootless-only commands, private loopback binding, release activation ordering, and exact-digest rollback.
3. Implement the deployment script, private runtime Compose file, and serialized main-only GitHub deployment workflow.
4. Run the focused local checks and existing CI-equivalent application checks. No OCI deployment or production connection is part of local validation.
