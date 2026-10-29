"""Publish an immutable, digest-bound container archive for the Mac pull deployer."""
import argparse, datetime, hashlib, json, os, pathlib, re, subprocess, tarfile, urllib.error, urllib.request

API = 'https://api.github.com'

def request(path, token, data=None, method=None, binary=False):
    url = path if path.startswith('https://uploads.github.com/') else API + path
    headers = {'Authorization': 'Bearer ' + token, 'User-Agent': 'EventiApp-Release/1.0',
               'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28'}
    if data is not None:
        headers['Content-Type'] = 'application/octet-stream' if binary else 'application/json'
        if not binary: data = json.dumps(data).encode()
    with urllib.request.urlopen(urllib.request.Request(url, data=data, headers=headers, method=method), timeout=300) as response:
        return json.load(response)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--service', choices=['api', 'workers'], required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--archive', type=pathlib.Path, required=True)
    args = parser.parse_args()
    repo = os.environ['GITHUB_REPOSITORY']; revision = os.environ.get('RELEASE_SHA', os.environ['GITHUB_SHA'])
    expected = 'Itbem-Corp/itbem-events-' + ('backend' if args.service == 'api' else 'workers')
    if repo != expected or not re.fullmatch('[a-f0-9]{40}', revision): raise RuntimeError('Invalid release source')
    token = os.environ['GH_TOKEN']
    main_ref = request('/repos/' + repo + '/git/ref/heads/main', token)['object']['sha']
    if main_ref != revision:
        print('A newer main revision exists; this release is superseded.')
        with open(os.environ['GITHUB_OUTPUT'], 'a') as output: output.write('published=false\n')
        return
    image = json.loads(subprocess.check_output(['docker', 'image', 'inspect', args.image]))[0]
    if image['Config'].get('Labels', {}).get('org.opencontainers.image.revision') != revision:
        raise RuntimeError('Image revision does not match validated source')
    if image['Architecture'] != 'amd64' or image['Os'] != 'linux': raise RuntimeError('Invalid image platform')
    digest = hashlib.sha256()
    with args.archive.open('rb') as archive:
        for chunk in iter(lambda: archive.read(1024 * 1024), b''): digest.update(chunk)
    size = args.archive.stat().st_size
    if not 0 < size <= 1024**3: raise RuntimeError('Container archive exceeds release limit')
    with tarfile.open(args.archive, 'r:*') as archive:
        entry = archive.getmember('manifest.json')
        if not entry.isfile() or entry.size > 65536: raise RuntimeError('Invalid Docker archive manifest')
        images = json.load(archive.extractfile(entry))
        if len(images) != 1 or images[0].get('RepoTags') != [args.image]: raise RuntimeError('Archive image tag mismatch')
        entry = archive.getmember(images[0]['Config'])
        if not entry.isfile() or not 0 < entry.size <= 4 * 1024 * 1024: raise RuntimeError('Invalid image configuration')
        config_digest = 'sha256:' + hashlib.sha256(archive.extractfile(entry).read()).hexdigest()
    run_id = os.environ['GITHUB_RUN_ID']; attempt = os.environ['GITHUB_RUN_ATTEMPT']
    if not run_id.isdigit() or not attempt.isdigit(): raise RuntimeError('Invalid workflow identity')
    tag = 'mac-' + args.service + '-' + revision + '-' + run_id + '-' + attempt
    manifest = {'schema_version': 1, 'service': args.service, 'repository': repo,
                'source_revision': revision, 'image_tag': args.image, 'image_id': image['Id'],
                'image_config_digest': config_digest,
                'archive_sha256': digest.hexdigest(), 'archive_size': size,
                'platform': 'linux/amd64', 'workflow_run_id': int(run_id),
                'workflow_run_attempt': int(attempt), 'release_tag': tag}
    release = request('/repos/' + repo + '/releases', token,
                      {'tag_name': tag, 'target_commitish': revision, 'name': tag,
                       'body': 'Validated Mac mini ' + args.service + ' image at ' + revision,
                       'draft': True, 'prerelease': False, 'make_latest': 'false'})
    upload = release['upload_url'].split('{', 1)[0]
    request(upload + '?name=release-manifest.json', token, json.dumps(manifest).encode(), binary=True)
    request(upload + '?name=docker-image.tar.gz', token, args.archive.read_bytes(), binary=True)
    request('/repos/' + repo + '/releases/' + str(release['id']), token,
            {'draft': False, 'make_latest': 'false'}, method='PATCH')
    with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
        output.write('published=true\nrelease_url=' + release['html_url'] + '\n')
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as output:
        output.write('### Mac mini release published\n\n' + release['html_url'] + '\n\n'
                     'Revision: `' + revision + '`\n\nArchive SHA-256: `' + digest.hexdigest() + '`\n')
    print('Published digest-bound release ' + tag)

if __name__ == '__main__': main()
