"""Real ext4 preparation checks; the substituted boot runner does not start a VM."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


@unittest.skipIf(os.name != "posix" or os.geteuid() == 0, "requires non-root Linux")
class DisposableProofTest(unittest.TestCase):
    def test_preparation_preserves_base_and_refuses_existing_directory(self):
        for tool in ("debugfs", "mkfs.ext4", "bash"):
            self.assertIsNotNone(shutil.which(tool), f"required test tool missing: {tool}")
        source = Path(__file__).resolve().parents[1] / "test-firecracker-repository-command.sh"
        with tempfile.TemporaryDirectory(prefix="itbem-proof-test-") as directory:
            root = Path(directory)
            base = root / "base"
            base.mkdir()
            image = base / "hello-rootfs.ext4"
            with image.open("wb") as stream:
                stream.truncate(8 * 1024 * 1024)
            subprocess.run(["mkfs.ext4", "-q", "-F", str(image)], check=True)
            subprocess.run(["debugfs", "-w", "-R", "mkdir /sbin", str(image)], check=True, capture_output=True)
            (base / "hello-vmlinux.bin").write_bytes(b"synthetic kernel fixture")
            release = base / "release-v1.7.0-x86_64"
            release.mkdir()
            (release / "firecracker-v1.7.0-x86_64").write_bytes(b"synthetic unused binary")
            script = root / source.name
            shutil.copyfile(source, script)
            (root / "test-firecracker-roundtrip.sh").write_text(
                '#!/bin/bash\nset -eu\nprintf "ITBEM_REPOSITORY_COMMAND_PASS\\n" > "$1/itbem-firecracker.stdout"\n'
            )
            original = hashlib.sha256(image.read_bytes()).digest()
            work = root / "disposable"
            result = subprocess.run(["bash", str(script), str(base), str(work)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(hashlib.sha256(image.read_bytes()).digest(), original)
            self.assertNotEqual(hashlib.sha256((work / image.name).read_bytes()).digest(), original)
            self.assertTrue((work / "preparation" / "itbem-init").is_file())
            sentinel = work / "keep.txt"
            sentinel.write_text("preserve this directory")
            result = subprocess.run(["bash", str(script), str(base), str(work)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(sentinel.read_text(), "preserve this directory")
            self.assertEqual(hashlib.sha256(image.read_bytes()).digest(), original)


if __name__ == "__main__":
    unittest.main()
