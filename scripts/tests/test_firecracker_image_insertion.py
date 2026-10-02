"""Reject debugfs false success on merged-/usr rootfs layouts."""
import importlib.util
import json
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
        events = [{'Action': 'run', 'Package': 'fixture', 'Test': 'TestReal'},
                  {'Action': 'pass', 'Package': 'fixture', 'Test': 'TestReal'},
                  {'Action': 'pass', 'Package': 'fixture'}]
        valid = {'ok': True, 'executed': True, 'exit_code': 0,
                 'stdout': '\n'.join(json.dumps(event) for event in events)}
        self.assertTrue(module.verified_guest_execution(valid))
        self.assertTrue(module.verified_guest_execution({'ok': False, 'executed': True, 'exit_code': 1, 'error': 'guest command failed'}))
        for invalid in (
            dict(valid, stdout=''),
            dict(valid, stdout=json.dumps({'Action': 'skip', 'Test': 'TestReal'})),
            dict(valid, stdout=json.dumps(events[-1])),
            dict(valid, executed=False),
            dict(valid, exit_code=True),
            dict(valid, error='guest output exceeded limit'),
            dict(valid, stdout='x' * 12001),
            dict(valid, stdout='\ud800'),
            dict(valid, stdout=json.dumps(events[1]) + '\n' + json.dumps(events[2])),
        ):
            self.assertFalse(module.verified_guest_execution(invalid))
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
            executable = root / 'owned-sleep'
            shutil.copyfile(shutil.which('sleep'), executable)
            executable.chmod(0o700)
            child = subprocess.Popen([str(executable), '30'], env={})
            try:
                pid_file = root / 'owned.pid'
                pid_file.write_text(str(child.pid))
                with self.assertRaisesRegex(RuntimeError, 'PID namespace'):
                    module.open_jailed_process(pid_file, executable)
                with self.assertRaisesRegex(RuntimeError, 'executable'):
                    module.open_jailed_process(pid_file, root / 'different-executable', require_namespace=False)
                descriptor = module.open_jailed_process(pid_file, executable, require_namespace=False)
                try:
                    self.assertTrue(module.stop_jailed_process(descriptor))
                finally:
                    os.close(descriptor)
                self.assertNotEqual(child.wait(timeout=3), 0)
            finally:
                if child.poll() is None:
                    child.kill()
                child.wait()


if __name__ == '__main__':
    unittest.main()
