#!/usr/bin/env python3
"""Release committed main source without changing unrelated production settings."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import zipfile

REGION = 'ca-central-1'
STACK = 'TransactionsEngine'
ACCOUNT = '187489282488'
BUCKET = f'cdk-hnb659fds-assets-{ACCOUNT}-{REGION}'
REGISTRY = f'{ACCOUNT}.dkr.ecr.{REGION}.amazonaws.com'
REPOSITORY = f'cdk-hnb659fds-container-assets-{ACCOUNT}-{REGION}'
ROOT = Path(__file__).resolve().parents[1]


def run(*args, **kwargs):
    return subprocess.check_output(args, cwd=ROOT, text=True, **kwargs).strip()


def aws(*args):
    value = run('aws', '--region', REGION, *args)
    return json.loads(value) if value else {}


def upload(path, prefix):
    key = f'{prefix}/{hashlib.sha256(path.read_bytes()).hexdigest()}{path.suffix}'
    aws('s3api', 'put-object', '--bucket', BUCKET, '--key', key,
        '--body', str(path), '--server-side-encryption', 'AES256')
    return key


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('phase', choices=['release', 'cutover'])
    args = parser.parse_args()
    if run('git', 'branch', '--show-current') != 'main':
        raise SystemExit('Deployment requires the main branch')
    if run('git', 'status', '--porcelain'):
        raise SystemExit('Commit local changes before deployment')
    revision = run('git', 'rev-parse', 'HEAD')
    remote = run('git', '-c', 'credential.helper=', '-c',
                 'credential.helper=!gh auth git-credential', 'ls-remote',
                 'https://github.com/nathanfredericks/transactions.git',
                 'refs/heads/main').split()[0]
    if remote != revision:
        raise SystemExit('Push this main revision before deployment')
    if aws('cloudformation', 'describe-stacks', '--stack-name', STACK)['Stacks'][0]['StackStatus'] not in ['UPDATE_COMPLETE', 'CREATE_COMPLETE']:
        raise SystemExit('Stack must be stable before deployment')
    template = aws('cloudformation', 'get-template', '--stack-name', STACK,
                   '--template-stage', 'Original')['TemplateBody']
    if isinstance(template, str):
        template = json.loads(template)
    original = json.loads(json.dumps(template))
    resources = template['Resources']
    prefix = f'main/{revision}/{args.phase}'
    output = ROOT / 'dist' / 'main-release'
    output.mkdir(parents=True, exist_ok=True)

    if args.phase == 'release':
        bootstrap = output / 'bootstrap'
        subprocess.run(['go', 'build', '-trimpath', '-ldflags=-s -w', '-o', str(bootstrap), './cmd/lambda'],
                       cwd=ROOT, env={**os.environ, 'GOOS': 'linux', 'GOARCH': 'arm64', 'CGO_ENABLED': '0'}, check=True)
        archive = output / 'lambda.zip'
        with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as z:
            z.write(bootstrap, 'bootstrap')
        code_key = upload(archive, prefix)
        for name in ['gateway', 'processor']:
            properties = resources[name]['Properties']
            properties['Code'] = {'S3Bucket': BUCKET, 'S3Key': code_key}
            properties['Environment']['Variables'].update(DEPLOYMENT_COMMIT=revision, DEPLOYMENT_BRANCH='main')

        license_file = Path(os.environ.get('CLOAKBROWSER_LICENSE_FILE', Path.home() / '.cloakbrowser/license.key'))
        if not license_file.is_file():
            raise SystemExit('The private browser license file is required')
        image = f'{REGISTRY}/{REPOSITORY}:main-{revision}'
        # Application releases retain the deployed, immutable browser runtime.
        # Refreshing browser binaries is a separate, deliberately verified change.
        browser_base = resources['BrowserTask']['Properties']['ContainerDefinitions'][0]['Image']
        if '@sha256:' not in browser_base:
            raise SystemExit('The deployed browser runtime must use an immutable image digest')
        subprocess.run(['docker', 'build', '--platform', 'linux/arm64', '--secret',
                        f'id=cloak_license,src={license_file}', '--build-arg',
                        f'BROWSER_RUNTIME_IMAGE={browser_base}', '-t', image, '.'], cwd=ROOT, check=True)
        password = run('aws', '--region', REGION, 'ecr', 'get-login-password')
        with tempfile.TemporaryDirectory(prefix='transactions-ecr-') as directory:
            auth = Path(directory) / 'config.json'
            auth.write_text(json.dumps({'auths': {REGISTRY: {'auth': base64.b64encode(('AWS:' + password).encode()).decode()}}}))
            auth.chmod(0o600)
            subprocess.run(['docker', '--config', directory, 'push', image], cwd=ROOT, check=True)
        digest = aws('ecr', 'describe-images', '--repository-name', REPOSITORY,
                     '--image-ids', f'imageTag=main-{revision}')['imageDetails'][0]['imageDigest']
        container = resources['BrowserTask']['Properties']['ContainerDefinitions'][0]
        container['Image'] = f'{REGISTRY}/{REPOSITORY}@{digest}'
        environment = {item['Name']: item['Value'] for item in container['Environment']}
        environment.update(DEPLOYMENT_COMMIT=revision, DEPLOYMENT_BRANCH='main', BROWSER_BROKER_URL='https://browser-broker.fredericks.app')
        container['Environment'] = [{'Name': key, 'Value': value} for key, value in environment.items()]
        processor_statements = resources['ProcessorRole']['Properties']['Policies'][0]['PolicyDocument']['Statement']
        if not any(s.get('Sid') == 'CapacityNotificationWorkflow' for s in processor_statements):
            processor_statements.append({'Sid':'CapacityNotificationWorkflow','Effect':'Allow','Action':'states:StartExecution','Resource':f'arn:aws:states:{REGION}:{ACCOUNT}:stateMachine:transactions-engine'})
        broker_secret = aws('secretsmanager', 'describe-secret', '--secret-id', 'transactions-engine/browser-broker')['ARN']
        container['Secrets'] = [v for v in container.get('Secrets', []) if v['Name'] != 'BROWSER_BROKER_TOKEN']
        container['Secrets'].append({'Name': 'BROWSER_BROKER_TOKEN', 'ValueFrom': broker_secret})
        statements = resources['TaskExecutionRole']['Properties']['Policies'][0]['PolicyDocument']['Statement']
        if not any(s.get('Sid') == 'BrowserBrokerSecret' for s in statements):
            statements.append({'Sid':'BrowserBrokerSecret','Effect':'Allow','Action':'secretsmanager:GetSecretValue','Resource':broker_secret})

        # Stage the main branch while the old branch continues serving the domain.
        if 'AdminBranchMain' not in resources:
            resources['AdminBranchMain'] = json.loads(json.dumps(resources['AdminBranch']))
            resources['AdminBranchMain']['Properties']['BranchName'] = 'main'
        # The operator requested Pushover notifications without AWS alarm email.
        resources.pop('OperatorEmail', None)
    else:
        if 'AdminBranchMain' not in resources:
            raise SystemExit('Release and build the main admin branch first')
        app_id = next(o['OutputValue'] for o in aws('cloudformation', 'describe-stacks', '--stack-name', STACK)['Stacks'][0]['Outputs'] if o['OutputKey'] == 'OutputAdminApp')
        jobs = aws('amplify', 'list-jobs', '--app-id', app_id, '--branch-name', 'main', '--max-results', '1')['jobSummaries']
        if not jobs or jobs[0]['status'] != 'SUCCEED' or jobs[0]['commitId'] != revision:
            raise SystemExit('The current main commit must have a successful Amplify build before cutover')
        domain = resources['AdminDomain']
        for mapping in domain['Properties']['SubDomainSettings']:
            mapping['BranchName'] = 'main'
        domain['DependsOn'] = ['AdminBranchMain']
        resources.pop('AdminBranch', None)

    changed = sorted(name for name in original['Resources'].keys() | resources.keys()
                     if original['Resources'].get(name) != resources.get(name))
    allowed = {'gateway', 'processor', 'BrowserTask', 'AdminBranchMain', 'OperatorEmail', 'TaskExecutionRole', 'ProcessorRole'} if args.phase == 'release' else {'AdminDomain', 'AdminBranch'}
    if not set(changed) <= allowed:
        raise SystemExit(f'Unexpected resource changes: {changed}')
    path = output / f'{args.phase}-template.json'
    path.write_text(json.dumps(template, indent=2)); path.chmod(0o600)
    template_key = upload(path, prefix)
    url = f'https://{BUCKET}.s3.{REGION}.amazonaws.com/{template_key}'
    metadata = {'revision': revision, 'branch': 'main', 'phase': args.phase, 'changedResources': changed, 'templateUrl': url}
    if args.phase == 'release':
        metadata.update(lambdaCodeHash=base64.b64encode(hashlib.sha256(archive.read_bytes()).digest()).decode(), imageDigest=digest)
    (output / f'{args.phase}.json').write_text(json.dumps(metadata, indent=2))
    name = f'main-{args.phase}-{revision[:12]}'
    aws('cloudformation', 'create-change-set', '--stack-name', STACK, '--change-set-name', name,
        '--change-set-type', 'UPDATE', '--template-url', url, '--capabilities', 'CAPABILITY_IAM', 'CAPABILITY_NAMED_IAM')
    subprocess.run(['aws', '--region', REGION, 'cloudformation', 'wait', 'change-set-create-complete',
                    '--stack-name', STACK, '--change-set-name', name], check=True)
    changes = aws('cloudformation', 'describe-change-set', '--stack-name', STACK, '--change-set-name', name)
    print(json.dumps(metadata, indent=2), flush=True)
    # CloudFormation also lists indirect references to modified resources.
    if any(c['ResourceChange'].get('Replacement') == 'True' and c['ResourceChange']['LogicalResourceId'] not in {'BrowserTask'} for c in changes['Changes']):
        raise SystemExit('Unexpected resource replacement; inspect the prepared change set')
    aws('cloudformation', 'execute-change-set', '--stack-name', STACK, '--change-set-name', name)
    print('Deployment started; verify UPDATE_COMPLETE and the main package hashes.', flush=True)


if __name__ == '__main__':
    main()
