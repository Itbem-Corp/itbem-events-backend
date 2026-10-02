"""Reject debugfs false success on merged-/usr rootfs layouts."""
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


@unittest.skipIf(os.name != 'posix', 'requires Linux ext4 tools')
class ImageInsertionTest(unittest.TestCase):
    def test_symlink_parent_is_rejected_and_direct_path_is_verified(self):
        for tool in ('debugfs', 'mkfs.ext4'):
            self.assertIsNotNone(shutil.which(tool), f'required tool missing: {tool}')
        scripts = Path(__file__).resolve().parents[1]
        sys.path.insert(0, str(scripts))
        self.addCleanup(lambda: sys.path.remove(str(scripts)))
        spec = importlib.util.spec_from_file_location('fixture_supervisor', scripts / 'firecracker-supervisor-vsock.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        for response, expected, valid in (
            ({'ok': True, 'stdout': 'exact\n'}, b'exact\n', True),
            ({'ok': True, 'stdout': ''}, b'', True),
            ({'ok': True, 'stdout': 'unrelated'}, b'exact\n', False),
            ({'ok': 'true', 'stdout': 'exact'}, b'exact', False),
            ({'ok': True, 'stdout': 'exact', 'error': 'failed'}, b'exact', False),
            ({'ok': True, 'stdout': 'x' * 12001}, b'x' * 12001, False),
            ({'ok': True, 'stdout': '\ud800'}, b'exact', False),
        ):
            self.assertEqual(module.verified_guest_read(response, expected), valid)
        with tempfile.TemporaryDirectory(prefix='itbem-image-insertion-') as directory:
            root = Path(directory)
            image = root / 'image.ext4'
            with image.open('wb') as stream:
                stream.truncate(8 * 1024 * 1024)
            subprocess.run(['mkfs.ext4', '-q', '-F', str(image)], check=True)
            for command in ('mkdir /usr', 'mkdir /usr/sbin', 'symlink /sbin usr/sbin'):
                subprocess.run(['debugfs', '-w', '-R', command, str(image)], check=True, capture_output=True)
            source = root / 'init'
            source.write_bytes(b'#!/bin/sh\necho fixture\n')
            with self.assertRaisesRegex(RuntimeError, 'insertion verification failed'):
                module.debugfs_write(image, source, '/sbin/itbem-init')
            module.debugfs_write(image, source, '/itbem-init')
            source.write_bytes(b'different bytes')
            with self.assertRaisesRegex(RuntimeError, 'insertion verification failed'):
                module.debugfs_write(image, source, '/itbem-init')


if __name__ == '__main__':
    unittest.main()
