import importlib.util, json, pathlib, tempfile, unittest, urllib.request
from unittest import mock

spec = importlib.util.spec_from_file_location('pull_release', pathlib.Path(__file__).with_name('pull-mac-release.py'))
pull = importlib.util.module_from_spec(spec); spec.loader.exec_module(pull)

class PullReleaseTests(unittest.TestCase):
    def fixture(self):
        revision = 'a' * 40
        tag = 'mac-api-' + revision + '-123-1'
        manifest = {'schema_version': 1, 'service': 'api', 'repository': pull.SERVICES['api'],
                    'source_revision': revision, 'platform': 'linux/amd64', 'image_id': 'sha256:' + 'b' * 64,
                    'archive_sha256': 'c' * 64, 'archive_size': 1024, 'workflow_run_id': 123,
                    'workflow_run_attempt': 1, 'release_tag': tag}
        return manifest, {'tag_name': tag, 'draft': False, 'prerelease': False}, revision

    def test_rejects_stale_wrong_repository_and_unapproved_releases(self):
        manifest, release, revision = self.fixture()
        pull.validate_manifest(manifest, release, 'api', revision)
        for field, value in [('repository', pull.SERVICES['workers']), ('source_revision', 'd' * 40),
                             ('archive_sha256', 'not-a-digest'), ('archive_size', 1024**3 + 1),
                             ('release_tag', 'forged'), ('image_id', 'mutable:latest')]:
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                pull.validate_manifest({**manifest, field: value}, release, 'api', revision)
        with self.assertRaises(RuntimeError):
            pull.validate_manifest(manifest, {**release, 'draft': True}, 'api', revision)

    def test_download_redirect_never_forwards_installation_token(self):
        original = urllib.request.Request('https://api.github.com/repos/a/b/releases/assets/1', headers={'Authorization': 'Bearer synthetic'})
        target = pull.SafeRedirect().redirect_request(original, None, 302, 'Found', {}, 'https://release-assets.githubusercontent.com/asset')
        self.assertFalse(target.has_header('Authorization'))
        with self.assertRaises(RuntimeError):
            pull.SafeRedirect().redirect_request(original, None, 302, 'Found', {}, 'https://attacker.invalid/asset')

    def test_failed_promotion_restores_previous_environment_and_verifies_rollback(self):
        manifest, _, _ = self.fixture()
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory); original = b'API_IMAGE=old-image\nAPI_REVISION=' + b'd' * 40 + b'\nWORKERS_IMAGE=keep-workers\n'
            (root/'.env').write_bytes(original)
            image = {'Architecture': 'amd64', 'Os': 'linux', 'Config': {'User': 'appuser', 'Labels': {'org.opencontainers.image.revision': manifest['source_revision']}}}
            with mock.patch.object(pull, 'ROOT', root), mock.patch.object(pull.subprocess, 'check_output', return_value=json.dumps([image]).encode()), mock.patch.object(pull.subprocess, 'run'), mock.patch.object(pull, 'compose') as compose, mock.patch.object(pull, 'wait_healthy', side_effect=[False, True]) as ready:
                with self.assertRaisesRegex(RuntimeError, 'previous image restored and healthy'): pull.promote('api', manifest)
                self.assertEqual((root/'.env').read_bytes(), original)
                self.assertEqual(compose.call_count, 2)
                self.assertEqual(ready.call_args.args, ('api', 'd' * 40))

    def test_loaded_image_with_wrong_revision_never_changes_configuration(self):
        manifest, _, _ = self.fixture()
        wrong = {'Architecture': 'amd64', 'Os': 'linux', 'Config': {'User': 'appuser', 'Labels': {'org.opencontainers.image.revision': 'e' * 40}}}
        with mock.patch.object(pull.subprocess, 'check_output', return_value=json.dumps([wrong]).encode()), mock.patch.object(pull, 'private_write') as write:
            with self.assertRaises(RuntimeError): pull.promote('api', manifest)
            write.assert_not_called()

    def test_successful_promotion_preserves_tenant_auth_queues_and_provider_keys(self):
        manifest, _, revision = self.fixture()
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory); (root/'secrets/ai-provider').mkdir(parents=True)
            (root/'.env').write_text('API_IMAGE=old\nAPI_REVISION=' + 'd' * 40 + '\nWORKERS_IMAGE=unchanged\n')
            protected = {'secrets/api.env': b'ENV=production\nTENANT_HOST_MAP=all-three-tenants\nSQS_AUTOMATION_QUEUE_URL=existing-queue\nAUTOMATION_CALLBACK_SECRET=synthetic-only\nAUTOMATION_WORKSPACE_CATALOG_POLICY_JSON={"policy":"existing"}\n',
                         'secrets/workers.env': b'SQS_WORKER_QUEUE_URL=existing-worker-queue\n',
                         'secrets/ai-provider/credentials.json': b'{"schema_version":2,"credentials":{"minimax":{"api_key":"synthetic-only"}}}'}
            for name, value in protected.items():
                (root/name).write_bytes(value); (root/name).chmod(0o600)
            image = {'Architecture': 'amd64', 'Os': 'linux', 'Config': {'User': 'appuser', 'Labels': {'org.opencontainers.image.revision': revision}}}
            with mock.patch.object(pull, 'ROOT', root), mock.patch.object(pull.subprocess, 'check_output', return_value=json.dumps([image]).encode()), mock.patch.object(pull.subprocess, 'run'), mock.patch.object(pull, 'compose'), mock.patch.object(pull, 'wait_healthy', return_value=True):
                active = pull.promote('api', manifest)
            self.assertEqual(active['revision'], revision)
            for name, value in protected.items(): self.assertEqual((root/name).read_bytes(), value)
            self.assertIn('WORKERS_IMAGE=unchanged', (root/'.env').read_text())

    def test_transient_download_failure_retries_without_restarting_production(self):
        manifest, release, revision = self.fixture()
        release.update({'id': 7, 'published_at': '2026-10-29', 'assets': [{'name': 'release-manifest.json'}, {'name': 'docker-image.tar.gz'}]})
        fake = mock.Mock()
        def metadata(path):
            if '/releases?' in path: return [release]
            if '/git/ref/heads/' in path: return {'object': {'sha': revision}}
            return {'object': {'type': 'commit', 'sha': revision}}
        def asset(value, destination=None):
            if destination is None: return manifest
            raise OSError('synthetic network failure')
        fake.json.side_effect = metadata; fake.asset.side_effect = asset
        state = {}
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory); (root/'deploy').mkdir()
            with mock.patch.object(pull, 'ROOT', root), mock.patch.object(pull, 'GitHub', return_value=fake), mock.patch.object(pull, 'compose') as compose:
                for _ in range(2):
                    with self.assertRaises(OSError): pull.poll('api', {}, state)
                    self.assertNotIn('rejected_release_id', state['api'])
                compose.assert_not_called()
        self.assertEqual(fake.asset.call_count, 4)

    def test_rejected_release_is_not_retried_every_poll(self):
        fake = mock.Mock()
        fake.json.return_value = [{'id': 7, 'tag_name': 'mac-api-' + 'a' * 40 + '-1-1', 'draft': False, 'prerelease': False, 'published_at': '2026-10-29'}]
        with mock.patch.object(pull, 'GitHub', return_value=fake):
            self.assertEqual(pull.poll('api', {}, {'api': {'rejected_release_id': 7}}), 'rejected release held')
            fake.asset.assert_not_called()

if __name__ == '__main__': unittest.main()
