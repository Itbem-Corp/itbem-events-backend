"""Pull approved GitHub releases. No job executor, listener, or runner runs here."""
import argparse, base64, fcntl, hashlib, json, os, pathlib, re, subprocess, tarfile, tempfile, time, urllib.error, urllib.parse, urllib.request

ROOT = pathlib.Path('/home/andbema/eventiapp')
SERVICES = {'api': 'Itbem-Corp/itbem-events-backend', 'workers': 'Itbem-Corp/itbem-events-workers'}
SHA = re.compile(r'^[a-f0-9]{40}$')
IMAGE = re.compile(r'^sha256:[a-f0-9]{64}$')

class SafeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, newurl):
        target = urllib.parse.urlparse(newurl)
        if target.scheme != 'https' or not (target.hostname == 'github.com' or target.hostname.endswith('.githubusercontent.com')):
            raise RuntimeError('Unexpected release download redirect')
        redirected = super().redirect_request(request, fp, code, msg, headers, newurl)
        if target.hostname != urllib.parse.urlparse(request.full_url).hostname:
            redirected.remove_header('Authorization')
        return redirected

def private_write(path, value):
    fd, temporary = tempfile.mkstemp(dir=path.parent, prefix='.' + path.name + '-')
    try:
        with os.fdopen(fd, 'wb') as output:
            output.write(value); output.flush(); os.fsync(output.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_DIRECTORY)
        try: os.fsync(directory)
        finally: os.close(directory)
    finally:
        if os.path.exists(temporary): os.unlink(temporary)

class GitHub:
    def __init__(self, repo, config):
        identity = config['repositories'][repo]
        def b64(value): return base64.urlsafe_b64encode(json.dumps(value, separators=(',', ':')).encode()).rstrip(b'=')
        payload = b64({'alg': 'RS256', 'typ': 'JWT'}) + b'.' + b64({'iat': int(time.time()) - 60, 'exp': int(time.time()) + 540, 'iss': identity['app_id']})
        with tempfile.NamedTemporaryFile(dir=ROOT/'deploy', prefix='.github-app-key-') as key:
            key.write(identity['private_key'].encode()); key.flush()
            signature = subprocess.check_output(['openssl', 'dgst', '-sha256', '-sign', key.name], input=payload, stderr=subprocess.DEVNULL)
        jwt = (payload + b'.' + base64.urlsafe_b64encode(signature).rstrip(b'=')).decode()
        self.opener = urllib.request.build_opener(SafeRedirect())
        self.token = jwt
        response = self.json('/app/installations/' + str(identity['installation_id']) + '/access_tokens',
                             {'repositories': [repo.split('/')[1]], 'permissions': {'contents': 'read'}})
        if response['permissions'].get('contents') != 'read': raise RuntimeError('Release token must be read-only')
        self.token = response['token']; self.repo = repo

    def open(self, path, data=None, binary=False):
        if not path.startswith('/') or path.startswith('//'): raise RuntimeError('Invalid GitHub API path')
        headers = {'Authorization': 'Bearer ' + self.token, 'User-Agent': 'EventiApp-Deploy/1.0',
                   'Accept': 'application/octet-stream' if binary else 'application/vnd.github+json',
                   'X-GitHub-Api-Version': '2022-11-28'}
        if data is not None: headers['Content-Type'] = 'application/json'
        return self.opener.open(urllib.request.Request('https://api.github.com' + path, data=json.dumps(data).encode() if data is not None else None, headers=headers), timeout=60)

    def json(self, path, data=None):
        with self.open(path, data) as response:
            raw = response.read(2 * 1024 * 1024 + 1)
            if len(raw) > 2 * 1024 * 1024: raise RuntimeError('GitHub metadata exceeds limit')
            return json.loads(raw)

    def asset(self, asset, destination=None):
        expected = 'https://api.github.com/repos/' + self.repo + '/releases/assets/' + str(asset['id'])
        if asset['url'] != expected: raise RuntimeError('Unexpected release asset URL')
        with self.open(expected.removeprefix('https://api.github.com'), binary=True) as response:
            if destination is None:
                value = response.read(16385)
                if len(value) > 16384: raise RuntimeError('Manifest exceeds limit')
                return json.loads(value)
            digest = hashlib.sha256(); size = 0
            with destination.open('wb') as output:
                while chunk := response.read(1024 * 1024):
                    size += len(chunk)
                    if size > 1024**3: raise RuntimeError('Release download exceeds limit')
                    digest.update(chunk); output.write(chunk)
                output.flush(); os.fsync(output.fileno())
            return digest.hexdigest(), size

def validate_manifest(manifest, release, service, current_main):
    revision = manifest.get('source_revision', '')
    if manifest.get('schema_version') != 1 or manifest.get('service') != service or manifest.get('repository') != SERVICES[service]:
        raise RuntimeError('Release manifest identity mismatch')
    if not SHA.fullmatch(revision) or revision != current_main: raise RuntimeError('Release is not the current main revision')
    if manifest.get('platform') != 'linux/amd64' or not IMAGE.fullmatch(manifest.get('image_id', '')):
        raise RuntimeError('Release image identity mismatch')
    expected_tag = 'eventiapp-' + service + ':sha-' + revision
    if manifest.get('image_tag') != expected_tag or not IMAGE.fullmatch(manifest.get('image_config_digest', manifest['image_id'])):
        raise RuntimeError('Release image tag or configuration identity mismatch')
    if not re.fullmatch('[a-f0-9]{64}', manifest.get('archive_sha256', '')) or not 0 < manifest.get('archive_size', 0) <= 1024**3:
        raise RuntimeError('Invalid release archive digest or size')
    expected = 'mac-' + service + '-' + revision + '-' + str(manifest.get('workflow_run_id')) + '-' + str(manifest.get('workflow_run_attempt'))
    if not re.fullmatch(r'mac-' + service + r'-[a-f0-9]{40}-[0-9]+-[0-9]+', expected) or release.get('tag_name') != expected or manifest.get('release_tag') != expected:
        raise RuntimeError('Release tag does not identify its source and workflow')
    if release.get('draft') or release.get('prerelease'): raise RuntimeError('Unapproved release')

def validate_archive(path, manifest):
    # Docker's classic store identifies an image by its config digest; the
    # containerd store may expose an OCI manifest/index ID instead. Bind the
    # transport to the canonical config bytes, then use the loaded native ID.
    with tarfile.open(path, 'r:*') as archive:
        entry = archive.getmember('manifest.json')
        if not entry.isfile() or entry.size > 65536: raise RuntimeError('Invalid Docker archive manifest')
        images = json.load(archive.extractfile(entry))
        if len(images) != 1 or images[0].get('RepoTags') != [manifest['image_tag']]:
            raise RuntimeError('Docker archive tag mismatch')
        entry = archive.getmember(images[0]['Config'])
        if not entry.isfile() or not 0 < entry.size <= 4 * 1024 * 1024: raise RuntimeError('Invalid image configuration')
        raw = archive.extractfile(entry).read()
        digest = 'sha256:' + hashlib.sha256(raw).hexdigest()
        if digest != manifest.get('image_config_digest', manifest['image_id']):
            raise RuntimeError('Docker archive configuration digest mismatch')
        return json.loads(raw)

def compose(service):
    subprocess.run(['docker', 'compose', '--profile', 'application', 'up', '-d', '--no-deps', service], cwd=ROOT, check=True, timeout=420, stdout=subprocess.DEVNULL)

def request_health(url, host=None):
    headers = {'User-Agent': 'EventiApp-HealthCheck/1.0'}
    if host: headers['Host'] = host
    with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=10) as response:
        return json.load(response)

def healthy(service, revision, public=True):
    container = json.loads(subprocess.check_output(['docker', 'inspect', 'eventiapp-' + service + '-1']))[0]
    env = dict(x.split('=', 1) for x in container['Config']['Env'] if '=' in x)
    if not container['State']['Running'] or env.get('SOURCE_REVISION') != revision: return False
    port = '18080/health' if service == 'api' else '18091/ready'
    check = request_health('http://127.0.0.1:' + port, 'api.itbem.com.mx' if service == 'api' else None)
    def valid(value):
        if service == 'api':
            data = value.get('data', {})
            return data.get('db') == data.get('redis') == 'ok' and data.get('environment') == 'production'
        return value.get('status') == 'ready' and value.get('database_ready') and value.get('queue_healthy') and value.get('source_revision') == revision
    if not valid(check): return False
    if public:
        for domain in ['eventiapp.com.mx', 'itbem.com.mx', 'cafettonhouse.com']:
            url = 'https://' + ('api.' if service == 'api' else 'worker.') + domain + ('/health' if service == 'api' else '/ready')
            if not valid(request_health(url)): return False
    return True

def wait_healthy(service, revision):
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline:
        try:
            if healthy(service, revision): return True
        except Exception: pass
        time.sleep(3)
    return False

def promote(service, manifest, archive_config=None):
    revision = manifest['source_revision']
    image = json.loads(subprocess.check_output(['docker', 'image', 'inspect', manifest['image_tag']]))[0]
    image_id = image['Id']
    if not IMAGE.fullmatch(image_id): raise RuntimeError('Loaded image ID is invalid')
    if archive_config is not None and image.get('RootFS', {}).get('Layers') != archive_config.get('rootfs', {}).get('diff_ids'):
        raise RuntimeError('Loaded filesystem does not match the verified archive')
    if image['Architecture'] != 'amd64' or image['Os'] != 'linux' or image['Config'].get('Labels', {}).get('org.opencontainers.image.revision') != revision:
        raise RuntimeError('Loaded image does not match validated release')
    expected_user = 'appuser' if service == 'api' else '10001:10001'
    if image['Config'].get('User') != expected_user: raise RuntimeError('Unexpected container user')
    env_path = ROOT/'.env'; before = env_path.read_bytes()
    values = dict(x.split('=', 1) for x in before.decode().splitlines() if '=' in x)
    prefix = 'API' if service == 'api' else 'WORKERS'
    previous_revision = values[prefix + '_REVISION']
    values[prefix + '_IMAGE'] = image_id; values[prefix + '_REVISION'] = revision
    updated = ''.join(key + '=' + value + '\n' for key, value in values.items()).encode()
    try:
        private_write(env_path, updated)
        subprocess.run(['docker', 'compose', '--profile', 'application', 'config', '--quiet'], cwd=ROOT, check=True)
        compose(service)
        if not wait_healthy(service, revision): raise RuntimeError('Release readiness gate failed')
    except BaseException:
        private_write(env_path, before); compose(service)
        if not wait_healthy(service, previous_revision): raise RuntimeError('CRITICAL: rollback readiness gate failed') from None
        raise RuntimeError('Release rejected; previous image restored and healthy') from None
    return {'revision': revision, 'image_id': image_id, 'previous_revision': previous_revision,
            'previous_image': dict(x.split('=', 1) for x in before.decode().splitlines() if '=' in x)[prefix + '_IMAGE']}

def poll(service, config, state, retry=False, check=False):
    github = GitHub(SERVICES[service], config); base = '/repos/' + SERVICES[service]
    releases = github.json(base + '/releases?per_page=30')
    releases = [r for r in releases if r['tag_name'].startswith('mac-' + service + '-') and not r['draft'] and not r['prerelease']]
    if not releases: return 'no release published'
    release = max(releases, key=lambda r: r['published_at'])
    previous = state.get(service, {})
    if release['id'] == previous.get('release_id'): return 'already current'
    if release['id'] == previous.get('rejected_release_id') and not retry: return 'rejected release held'
    main_sha = github.json(base + '/git/ref/heads/main')['object']['sha']
    assets = {asset['name']: asset for asset in release['assets']}
    if set(assets) != {'release-manifest.json', 'docker-image.tar.gz'}: raise RuntimeError('Incomplete release assets')
    manifest = github.asset(assets['release-manifest.json'])
    if manifest.get('source_revision') != main_sha: return 'waiting for current main release'
    validate_manifest(manifest, release, service, main_sha)
    tag = github.json(base + '/git/ref/tags/' + release['tag_name'])['object']
    if tag['type'] != 'commit' or tag['sha'] != main_sha: raise RuntimeError('Release tag points to another commit')
    if check: return 'release validated; promotion disabled by --check'
    hold_release = False
    try:
        with tempfile.TemporaryDirectory(dir=ROOT/'deploy', prefix='.release-') as directory:
            archive = pathlib.Path(directory)/'docker-image.tar.gz'
            digest, size = github.asset(assets['docker-image.tar.gz'], archive)
            if digest != manifest['archive_sha256'] or size != manifest['archive_size']:
                hold_release = True
                raise RuntimeError('Release archive checksum mismatch')
            # Main may advance while downloading: never promote a stale build.
            if github.json(base + '/git/ref/heads/main')['object']['sha'] != main_sha: return 'release superseded while downloading'
            hold_release = True
            archive_config = validate_archive(archive, manifest)
            subprocess.run(['docker', 'image', 'load', '--input', str(archive)], check=True, stdout=subprocess.DEVNULL, timeout=240)
            active = promote(service, manifest, archive_config)
        state[service] = {**active, 'release_id': release['id'], 'deployed_at': int(time.time()), 'status': 'healthy'}
        return 'deployed ' + main_sha
    except Exception:
        state[service] = {**previous, 'status': 'failed', 'last_failure_at': int(time.time())}
        if hold_release: state[service]['rejected_release_id'] = release['id']
        raise

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--check', action='store_true'); parser.add_argument('--retry', action='store_true')
    args = parser.parse_args(); os.umask(0o077)
    deploy = ROOT/'deploy'; deploy.mkdir(mode=0o700, exist_ok=True)
    with (deploy/'deploy.lock').open('a') as lock:
        try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError: return
        config = json.loads((ROOT/'secrets/deploy-github-app.json').read_text())
        state_path = deploy/'state.json'
        state = json.loads(state_path.read_text()) if state_path.exists() else {}
        failed = False
        for service in SERVICES:
            try:
                outcome = poll(service, config, state, args.retry, args.check)
                if args.check or outcome.startswith('deployed'): print(service + ': ' + outcome)
            except Exception as error:
                failed = True
                # Do not serialize HTTP bodies, credentials, or raw subprocess logs.
                print(service + ': deployment failed (' + type(error).__name__ + ')')
            finally:
                if not args.check: private_write(state_path, (json.dumps(state, indent=2) + '\n').encode())
        if not args.check:
            metrics = []
            for service, value in state.items():
                metrics += ['eventiapp_deploy_last_success_timestamp_seconds{service="' + service + '"} ' + str(value.get('deployed_at', 0)),
                            'eventiapp_deploy_last_attempt_success{service="' + service + '"} ' + ('1' if value.get('status') == 'healthy' else '0')]
            private_write(ROOT/'monitoring/textfile/deploy.prom', ('\n'.join(metrics) + '\n').encode())
            # The exporter runs as another UID; metrics contain no private values.
            (ROOT/'monitoring/textfile/deploy.prom').chmod(0o644)
        if failed: raise SystemExit(1)

if __name__ == '__main__': main()
