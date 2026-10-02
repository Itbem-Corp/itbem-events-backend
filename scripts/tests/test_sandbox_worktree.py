import pathlib
import sys
import tempfile
import unittest
import hashlib
import json
import os
import subprocess

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from sandbox_worktree import snapshot_worktree


class WorktreeSnapshotTests(unittest.TestCase):
    def test_supervisors_reject_path_only_and_stale_source_binding(self):
        scripts = pathlib.Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root/'source.txt').write_text('approved\n')
            approved = snapshot_worktree(root)[0]
            (root/'source.txt').write_text('modified\n')
            for supervisor in ('firecracker-supervisor.py', 'firecracker-supervisor-vsock.py'):
                for digest in ('sha256:'+hashlib.sha256(str(root).encode()).hexdigest(), approved):
                    request = {'protocol_version': 1, 'operation': 'execute', 'lease_id': 'synthetic-lease', 'task_id': 'synthetic-task', 'workspace_id': 'synthetic-workspace', 'workspace_path': str(root), 'worktree_digest': digest}
                    observed = subprocess.run([sys.executable,str(scripts/supervisor)], input=json.dumps(request), text=True, capture_output=True, timeout=5, env={'PATH':os.defpath,'HOME':'/tmp'})
                    response = json.loads(observed.stdout)
                    self.assertFalse(response['ok'])
                    self.assertIn('bound source content', response['error'])

    def test_snapshot_binds_transferred_bytes_and_not_git_authority(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)/'source'
            root.mkdir()
            (root/'source.txt').write_text('approved\n')
            (root/'.git').mkdir()
            (root/'.git'/'credentials').write_text('synthetic-do-not-transfer')
            original = snapshot_worktree(root)[0]
            target = pathlib.Path(directory)/'snapshot'
            staged, count, _ = snapshot_worktree(root, target)
            self.assertEqual(staged, original)
            self.assertEqual(count, 1)
            self.assertFalse((target/'.git').exists())
            self.assertEqual((target/'source.txt').read_text(), 'approved\n')
            (root/'source.txt').write_text('modified\n')
            self.assertNotEqual(snapshot_worktree(root)[0], original)
            self.assertEqual(snapshot_worktree(target)[0], original)

    def test_credentials_symlinks_and_oversize_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root/'.env.local').write_text('synthetic')
            with self.assertRaisesRegex(RuntimeError, 'credential'): snapshot_worktree(root)
            (root/'.env.local').unlink()
            (root/'.local').mkdir()
            with self.assertRaisesRegex(RuntimeError, 'credential'): snapshot_worktree(root)
            (root/'.local').rmdir()
            (root/'escape').symlink_to(root/'outside')
            with self.assertRaisesRegex(RuntimeError, 'regular'): snapshot_worktree(root)
            (root/'escape').unlink()
            with (root/'oversize').open('wb') as handle: handle.truncate(8*1024*1024+1)
            with self.assertRaisesRegex(RuntimeError, 'oversized'): snapshot_worktree(root)


if __name__ == '__main__': unittest.main()
