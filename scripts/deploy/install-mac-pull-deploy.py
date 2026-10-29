import pathlib,subprocess,json,os,re
os.umask(0o077)
root=pathlib.Path('/home/andbema/eventiapp')
deploy=root/'deploy';deploy.mkdir(mode=0o700,exist_ok=True);deploy.chmod(0o700)
settings=json.loads((root/'secrets/deploy-github-app.json').read_text())
assert set(settings['repositories'])=={'Itbem-Corp/itbem-events-backend','Itbem-Corp/itbem-events-workers'}
state=json.loads(subprocess.check_output(['docker','inspect','eventiapp-api-1','eventiapp-workers-1']))
values={}
for item in state:
 service='API' if item['Name']=='/eventiapp-api-1' else 'WORKERS'
 env=dict(x.split('=',1) for x in item['Config']['Env'] if '=' in x)
 revision=env.get('SOURCE_REVISION','')
 if not re.fullmatch('[a-f0-9]{40}',revision):
  import urllib.request
  with urllib.request.urlopen('http://127.0.0.1:18091/ready',timeout=10) as response:revision=json.load(response)['source_revision']
 assert re.fullmatch('[a-f0-9]{40}',revision)
 values[service+'_IMAGE']=item['Image'];values[service+'_REVISION']=revision
envfile=root/'.env'
previous=dict(line.split('=',1) for line in envfile.read_text().splitlines() if '=' in line) if envfile.exists() else {}
previous.update(values)
envfile.write_text(''.join(key+'='+value+'\n' for key,value in previous.items()));envfile.chmod(0o600)
compose=root/'compose.yaml';source=compose.read_text()
if '${API_IMAGE' not in source:
 source=source.replace('image: eventiapp-api:local-ai-credentials','image: ${API_IMAGE:-eventiapp-api:local-ai-credentials}')
 source=source.replace("    volumes: ['./secrets/ai-provider:/run/itbem-ai-credentials:rw']", "    environment:\n      SOURCE_REVISION: ${API_REVISION}\n    volumes: ['./secrets/ai-provider:/run/itbem-ai-credentials:rw']")
 source=source.replace('image: eventiapp-workers:migration','image: ${WORKERS_IMAGE:-eventiapp-workers:migration}')
 source=source.replace('      HEALTH_BIND_ADDR: 0.0.0.0','      SOURCE_REVISION: ${WORKERS_REVISION}\n      HEALTH_BIND_ADDR: 0.0.0.0')
 compose.write_text(source)
assert '${API_IMAGE' in source and '${WORKERS_IMAGE' in source
subprocess.run(['docker','compose','--profile','application','config','--quiet'],cwd=root,check=True)
script=pathlib.Path(__file__).with_name('pull-mac-release.py')
assert script.is_file()
(deploy/'pull-mac-release.py').write_bytes(script.read_bytes());(deploy/'pull-mac-release.py').chmod(0o700)
backup=root/'backup-config.py';text=backup.read_text()
if "'deploy'" not in text:
 text=text.replace("'--exclude=monitoring/textfile'", "'--exclude=monitoring/textfile','--exclude=deploy/.release-*','--exclude=deploy/.github-app-key-*','--exclude=deploy/deploy.lock','--exclude=deploy/deploy.log'")
 text=text.replace("'compose.yaml','redirect.conf'", "'compose.yaml','.env','deploy','redirect.conf'")
 backup.write_text(text)
cron=subprocess.run(['crontab','-l'],capture_output=True,text=True)
lines=[line for line in cron.stdout.splitlines() if '/home/andbema/eventiapp/deploy/pull-mac-release.py' not in line]
lines.append('*/3 * * * * /usr/bin/python3 /home/andbema/eventiapp/deploy/pull-mac-release.py >> /home/andbema/eventiapp/deploy/deploy.log 2>&1')
subprocess.run(['crontab','-'],input='\n'.join(lines)+'\n',text=True,check=True)
print(json.dumps({'pullDeploymentInstalled':True,'pollIntervalMinutes':3,'runnerInstalled':False,'publicSshRequired':False,'secretsRemainLocal':True,'initialRevisions':{k:v for k,v in values.items() if k.endswith('_REVISION')}}))
