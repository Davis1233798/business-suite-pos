import re
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "infra" / "oci" / "deploy-release.sh"
GATEWAY = ROOT / "infra" / "oci" / "ssh-gateway.sh"
COMPOSE = ROOT / "infra" / "oci" / "compose.yaml"
WORKFLOW = ROOT / ".github" / "workflows" / "pos.yml"


class PosOciDeploymentContracts(unittest.TestCase):
    def test_deployer_requires_revision_and_sha256_digest(self):
        source = SCRIPT.read_text(encoding="utf-8")
        self.assertRegex(source, r"\[\[\s*\$sha\s*=~\s*\^\[0-9a-f\]\{40\}\$")
        self.assertRegex(source, r"\[\[\s*\$digest\s*=~\s*\^sha256:\[0-9a-f\]\{64\}\$\s*\]\]")

    def test_runtime_is_rootless_private_and_uses_an_isolated_data_directory(self):
        compose = COMPOSE.read_text(encoding="utf-8")
        self.assertIn("${POS_IMAGE:?", compose)
        self.assertIn("127.0.0.1:8095:8095", compose)
        self.assertIn("POS_DATA_DIR", compose)
        self.assertIn("pos-oci", SCRIPT.read_text(encoding="utf-8"))
        self.assertNotIn("/var/run/docker.sock", compose)

    def test_deployer_avoids_privileged_or_shared_suite_operations(self):
        source = SCRIPT.read_text(encoding="utf-8")
        self.assertNotRegex(source, r"\bsudo\b")
        self.assertNotIn("business-suite --", source)
        self.assertIn("DOCKER_HOST", source)
        self.assertIn("sha256:", source)
        self.assertIn("rollback", source.lower())
        self.assertIn("current", source)
        self.assertIn("shared/compose.yaml", source)
        self.assertNotIn("incoming/", source)
        self.assertIn("$(<\"$release/IMAGE\") == \"$image\"", source)
        self.assertIn('check_health "$(<"$previous/COMMIT")"', source)
        rollback = source.split("rollback() {", 1)[1].split("\n}\n", 1)[0]
        self.assertNotIn("|| true", rollback)

    def test_forced_command_gateway_allows_only_digest_deploy(self):
        gateway = GATEWAY.read_text(encoding="utf-8")
        self.assertIn("SSH_ORIGINAL_COMMAND", gateway)
        self.assertIn("^deploy\\ ([0-9a-f]{40})\\ (sha256:[0-9a-f]{64})$", gateway)
        self.assertNotIn("tar", gateway)
        self.assertIn("exec \"$deployer\" \"$sha\" \"$digest\"", gateway)

    def test_current_pointer_is_activated_after_health_acceptance(self):
        source = SCRIPT.read_text(encoding="utf-8")
        health = source.index("/api/health")
        activation = source.index('mv -Tf "$root/.current.$$" "$root/current"')
        self.assertLess(health, activation)
        self.assertRegex(source, r"docker\s+image\s+inspect.*--format.*Architecture")

    def test_main_deployment_is_digest_based_and_serialized(self):
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("group: pos-private-oci", workflow)
        self.assertIn("cancel-in-progress: false", workflow)
        self.assertIn('awk \'$1 == "Digest:"', workflow)
        self.assertIn('"deploy $GITHUB_SHA $IMAGE_DIGEST"', workflow)
        self.assertIn("OCI_POS_DEPLOY_KEY", workflow)
        self.assertIn("OCI_POS_HOST_KEY", workflow)
        self.assertIn("OCI_POS_HOST", workflow)
        self.assertIn("refs/heads/main", workflow)
        self.assertIn("vars.POS_DEPLOY_ENABLED == 'true'", workflow)
        self.assertIn("BatchMode=yes", workflow)
        self.assertNotRegex(workflow, r"(?i)public[_ -]?(?:url|route|ingress)")


class PosOciDeploymentRuntime(unittest.TestCase):
    def test_retry_and_failed_health_roll_back_to_previous_digest(self):
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("bash is unavailable")

        with tempfile.TemporaryDirectory(prefix="pos-oci-contract-") as temp:
            work = Path(temp)
            if os.name == "nt":
                drive, tail = os.path.splitdrive(os.path.abspath(temp))
                posix_work = f"/mnt/{drive[0].lower()}{tail.replace(os.sep, '/')}"
            else:
                posix_work = temp
            posix_root = f"{posix_work}/runtime"
            runtime_root = work / "runtime"
            bindir = work / "bin"
            bindir.mkdir()
            run_dir = work / "run"
            run_dir.mkdir()
            (run_dir / "docker.sock").touch()
            shared = runtime_root / "shared"
            shared.mkdir(parents=True)
            shutil.copyfile(COMPOSE, shared / "compose.yaml")

            source = SCRIPT.read_text(encoding="utf-8")
            source = source.replace(
                "readonly root=/srv/business-suite-pos",
                f"readonly root='{posix_root}'",
            )
            # The harness uses a regular placeholder file in place of a daemon socket.
            source = source.replace(
                '[[ -S "$XDG_RUNTIME_DIR/docker.sock" ]]',
                '[[ -e "$XDG_RUNTIME_DIR/docker.sock" ]]',
            )
            runner = work / "deploy-release.sh"
            runner.write_text(source, encoding="utf-8", newline="\n")
            runner_posix = f"{posix_work}/deploy-release.sh"

            self._write_executable(
                bindir / "id",
                '#!/usr/bin/env bash\n'
                'case "$1" in -un) echo pos ;; -u) echo 1001 ;; *) exit 2 ;; esac\n',
            )
            self._write_executable(
                bindir / "docker",
                '#!/usr/bin/env bash\n'
                'set -euo pipefail\n'
                'if [[ $1 == pull ]]; then exit 0; fi\n'
                'if [[ $1 == image && $2 == inspect ]]; then\n'
                '  if [[ $* == *Architecture* ]]; then echo arm64; else echo "$3"; fi\n'
                '  exit 0\n'
                'fi\n'
                'if [[ $1 == compose ]]; then\n'
                '  shift; envfile=""; action=""\n'
                '  while (($#)); do\n'
                '    if [[ $1 == --env-file ]]; then envfile=$2; shift 2; continue; fi\n'
                '    if [[ $1 == up || $1 == down ]]; then action=$1; break; fi\n'
                '    shift\n'
                '  done\n'
                '  if [[ $action == up ]]; then\n'
                '    revision=$(sed -n "s/^POS_REVISION=//p" "$envfile")\n'
                '    printf "%s\\n" "$revision" > "$FAKE_ACTIVE_REVISION"\n'
                '    [[ ${FAKE_FAIL_START_REVISION:-} != "$revision" ]] || exit 41\n'
                '  fi\n'
                '  exit 0\n'
                'fi\n'
                'exit 2\n',
            )
            self._write_executable(
                bindir / "curl",
                '#!/usr/bin/env bash\n'
                'revision=$(cat "$FAKE_ACTIVE_REVISION")\n'
                '[[ ${FAKE_FAIL_HEALTH_REVISION:-} != "$revision" ]] || exit 22\n'
                'printf \'{"data":{"revision":"%s"}}\\n\' "$revision"\n',
            )
            self._write_executable(
                bindir / "python3",
                '#!/usr/bin/env bash\nexec /usr/bin/python3 "$@"\n',
            )

            env = os.environ.copy()
            env["PATH"] = f"{posix_work}/bin:" + env.get("PATH", "")
            env["XDG_RUNTIME_DIR"] = f"{posix_work}/run"
            env["FAKE_ACTIVE_REVISION"] = f"{posix_work}/active-revision"
            (work / "active-revision").write_text("", encoding="utf-8")

            sha_a = "a" * 40
            sha_b = "b" * 40
            digest_a = "sha256:" + "1" * 64
            digest_b = "sha256:" + "2" * 64

            first = self._run_deployer(bash, runner_posix, sha_a, digest_a, env)
            self.assertEqual(first.returncode, 0, first.stderr)
            release_a = runtime_root / "releases" / sha_a
            current = runtime_root / "current"
            self.assertEqual(current.resolve().as_posix(), release_a.as_posix())
            self.assertEqual(
                (release_a / "IMAGE").read_text().strip(),
                f"ghcr.io/davis1233798/business-suite-pos-station@{digest_a}",
            )

            retry = self._run_deployer(bash, runner_posix, sha_a, digest_a, env)
            self.assertEqual(retry.returncode, 0, retry.stderr)

            failed_release = self._run_deployer(
                bash, runner_posix, sha_b, digest_b, {**env, "FAKE_FAIL_HEALTH_REVISION": sha_b}
            )
            self.assertNotEqual(failed_release.returncode, 0)
            self.assertIn("restoring the previous digest", failed_release.stderr)
            self.assertEqual(current.resolve().as_posix(), release_a.as_posix())
            self.assertEqual((work / "active-revision").read_text().strip(), sha_a)

            retry_b = self._run_deployer(bash, runner_posix, sha_b, digest_b, env)
            self.assertEqual(retry_b.returncode, 0, retry_b.stderr)
            self.assertEqual(current.resolve().name, sha_b)

    @staticmethod
    def _write_executable(path: Path, source: str):
        path.write_text(source, encoding="utf-8", newline="\n")
        path.chmod(0o755)

    @staticmethod
    def _run_deployer(bash, runner, sha, digest, env):
        return subprocess.run(
            [bash, str(runner), sha, digest],
            env=env,
            capture_output=True,
            text=True,
            timeout=30,
        )


if __name__ == "__main__":
    unittest.main()
