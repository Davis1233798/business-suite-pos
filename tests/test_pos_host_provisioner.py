import os
import shutil
import subprocess
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
PROVISIONER = ROOT / "infra" / "oci" / "provision-pos-rootless-host.sh"


class PosHostProvisionerContracts(unittest.TestCase):
    def test_versioned_idempotent_rootless_host_contract(self):
        source = PROVISIONER.read_text(encoding="utf-8")
        for contract in (
            "--check|--apply",
            "--deploy-public-key-file",
            "business-suite-pos/host-provisioner/v1",
            "/usr/local/libexec/mes-rootless/dockerd-rootless.sh",
            "/usr/local/libexec/mes-rootless/dockerd-rootless-setuptool.sh",
            "loginctl enable-linger",
            "DOCKER_CONFIG=/var/lib/pos/.docker",
            "XDG_RUNTIME_DIR=/run/user/$runtime_uid",
            "restrict,command=",
            "ForceCommand /usr/local/lib/business-suite-pos/ssh-gateway.sh",
            "cmp -s",
            "No changes made",
        ):
            with self.subTest(contract=contract):
                self.assertIn(contract, source)

    def test_provisioner_fails_closed_on_foreign_state_and_existing_accounts(self):
        source = PROVISIONER.read_text(encoding="utf-8")
        self.assertIn("refusing to adopt", source)
        self.assertIn("foreign or incomplete POS provisioner state", source)
        self.assertIn("invalid OpenSSH public key", source)
        self.assertIn("sshd -t", source)
        self.assertIn("sshd -T -C", source)
        self.assertIn("systemctl is-active docker.service", source)
        self.assertNotIn("docker compose up", source)
        self.assertNotIn("docker compose down", source)

    def test_ci_deployer_files_are_installed_root_owned_and_compared_to_source(self):
        source = PROVISIONER.read_text(encoding="utf-8")
        self.assertIn("/usr/local/lib/business-suite-pos/deploy-release.sh", source)
        self.assertIn("/usr/local/lib/business-suite-pos/compose.yaml", source)
        self.assertIn("install -o root -g root -m 0555", source)
        self.assertIn("install -o root -g root -m 0444", source)
        self.assertNotIn("rm -rf /srv/business-suite-pos", source)
        self.assertNotIn("userdel", source)

    def test_ssh_key_and_pos_data_are_isolated(self):
        source = PROVISIONER.read_text(encoding="utf-8")
        self.assertIn("readonly HOME=/var/lib/pos", source)
        self.assertIn("readonly DATA=/srv/business-suite-pos", source)
        self.assertIn("/srv/business-suite-pos/data", source)
        self.assertIn("/srv/business-suite-pos/releases", source)
        self.assertIn("/srv/business-suite-pos/shared", source)
        self.assertIn("no-port-forwarding", source)
        self.assertIn("no-agent-forwarding", source)
        self.assertIn("no-pty", source)

    @unittest.skipIf(os.name == "nt", "run this safe argument check from WSL/Linux")
    def test_unknown_option_exits_before_host_mutation(self):
        bash = shutil.which("bash")
        if not bash:
            self.skipTest("bash is unavailable")
        result = subprocess.run(
            [bash, str(PROVISIONER), "--unknown"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("Usage:", result.stderr)


if __name__ == "__main__":
    unittest.main()
