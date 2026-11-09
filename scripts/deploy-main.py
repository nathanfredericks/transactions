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


# Old per-purpose secrets folded into the single Transactions secret, and the
# deployment-only admin secrets now kept in 1Password item Transactions.
RETIRED_SECRETS = ['ApplicationSecret', 'Credentialseqbank', 'Credentialsnbdb', 'Credentialsrogersbank',
                   'CloakBrowserLicense', 'RepositoryConnection', 'AdminAuth0', 'AdminPassword']
# NoEcho parameter -> (1Password field in item Transactions, old secret id, JSON key in old secret)
ADMIN_PARAMETERS = {
    'GitHubToken': ('github_token', 'transactions-engine/github', 'token'),
    'Auth0ClientSecret': ('auth0_client_secret', 'transactions-engine/admin-auth0', 'clientSecret'),
    'Auth0SessionSecret': ('auth0_session_secret', 'transactions-engine/admin-auth0', 'sessionSecret'),
}
BROWSER_SECRETS = ['CLOAKBROWSER_LICENSE_KEY', 'BROWSER_BROKER_TOKEN']


def admin_parameter_values(source, vault):
    values = {}
    for name, (field, secret_id, key) in ADMIN_PARAMETERS.items():
        if source == 'old-secrets':
            value = json.loads(aws('secretsmanager', 'get-secret-value', '--secret-id', secret_id)['SecretString'])[key]
        else:
            value = subprocess.run(['op', 'read', f'op://{vault}/Transactions/{field}'],
                                   capture_output=True, text=True, check=True).stdout.strip()
        if not value:
            raise SystemExit(f'{name} is empty')
        values[name] = value
    return values


def migrate_secrets(template, secret_arn, auth0_client_id):
    """Point the deployed template at the Transactions secret and drop the old ones.

    Every removed secret is Retain/RetainExceptOnCreate, so CloudFormation leaves
    it in place; it is deleted separately after verification.
    """
    resources = template['Resources']
    missing = [name for name in RETIRED_SECRETS if name not in resources]
    if missing:
        raise SystemExit(f'Expected secrets are not in the deployed template: {missing}')
    for name in RETIRED_SECRETS:
        if resources[name].get('DeletionPolicy') not in ('Retain', 'RetainExceptOnCreate'):
            raise SystemExit(f'{name} is not retained; refusing to remove it')
        resources.pop(name)
    for name in ['gateway', 'processor']:
        resources[name]['Properties']['Environment']['Variables']['AWS_SECRET_ARN'] = secret_arn

    def secret_statements(role):
        return resources[role]['Properties']['Policies'][0]['PolicyDocument']['Statement']

    def point_secret_statements(role):
        statements = secret_statements(role)
        kept = [s for s in statements if s.get('Action') not in ('secretsmanager:GetSecretValue', ['secretsmanager:GetSecretValue'])]
        if len(statements) - len(kept) < 1:
            raise SystemExit(f'{role} has no secret statement to replace')
        kept.append({'Sid': 'TransactionsSecret', 'Effect': 'Allow', 'Action': 'secretsmanager:GetSecretValue', 'Resource': secret_arn})
        resources[role]['Properties']['Policies'][0]['PolicyDocument']['Statement'] = kept

    for role in ['ProcessorRole', 'GatewayRole', 'BrowserRole', 'TaskExecutionRole']:
        point_secret_statements(role)
    container = resources['BrowserTask']['Properties']['ContainerDefinitions'][0]
    container['Secrets'] = [{'Name': n, 'ValueFrom': f'{secret_arn}:{n}::'} for n in BROWSER_SECRETS]
    environment = [e for e in container['Environment'] if e['Name'] != 'AWS_SECRET_ARN']
    container['Environment'] = environment + [{'Name': 'AWS_SECRET_ARN', 'Value': secret_arn}]

    admin = resources['Admin']['Properties']
    if admin['BasicAuthConfig'].get('EnableBasicAuth') is not False:
        raise SystemExit('Admin basic authentication is still enabled; finish the Auth0 cutover first')
    admin['BasicAuthConfig'] = {'EnableBasicAuth': False}
    admin['AccessToken'] = {'Ref': 'GitHubToken'}
    replacements = {'AUTH0_CLIENT_ID': auth0_client_id,
                    'AUTH0_CLIENT_SECRET': {'Ref': 'Auth0ClientSecret'},
                    'AUTH0_SECRET': {'Ref': 'Auth0SessionSecret'}}
    for variable in admin['EnvironmentVariables']:
        if variable['Name'] in replacements:
            variable['Value'] = replacements.pop(variable['Name'])
    if replacements:
        raise SystemExit(f'Admin is missing environment variables {sorted(replacements)}')

    outputs = template.setdefault('Outputs', {})
    outputs.pop('OutputApplicationSecret', None)
    outputs['OutputSecret'] = {'Value': secret_arn}
    parameters = template.setdefault('Parameters', {})
    for name in ADMIN_PARAMETERS:
        parameters[name] = {'Type': 'String', 'NoEcho': True, 'MinLength': 1}
    leftover = [n for n in RETIRED_SECRETS if n in json.dumps(template)] + (['resolve:secretsmanager'] if 'resolve:secretsmanager' in json.dumps(template) else [])
    if leftover:
        raise SystemExit(f'References remain after migration: {leftover}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('phase', choices=['release', 'cutover', 'secrets'])
    parser.add_argument('--admin-values-from', choices=['1password', 'old-secrets'], default='1password',
                        help='secrets phase only: where to read the GitHub token and Auth0 secrets')
    parser.add_argument('--vault', default=os.environ.get('OP_VAULT', 'Personal'))
    parser.add_argument('--refresh-admin', action='store_true',
                        help='release only: read the admin parameters from 1Password instead of keeping the stack values (rotation)')
    parser.add_argument('--prepare-only', action='store_true', help='create and print the change set without executing it')
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

    if args.phase in ('release', 'secrets'):
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
        # Imports read YNAB live; undo the stored-mirror release (b317644):
        # its sync schedule, mirror storage, batch lookups and version expiry.
        resources.pop('ScheduleSyncYNAB', None)
        for role in ('GatewayRole', 'ProcessorRole'):
            statements = resources[role]['Properties']['Policies'][0]['PolicyDocument']['Statement']
            statements[:] = [s for s in statements if s.get('Sid') != 'YNABMirror']
            for statement in statements:
                if isinstance(statement['Action'], list) and 'dynamodb:BatchGetItem' in statement['Action']:
                    statement['Action'] = [a for a in statement['Action'] if a != 'dynamodb:BatchGetItem']
        rules = resources['PrivateState']['Properties']['LifecycleConfiguration']['Rules']
        rules[:] = [r for r in rules if r.get('Id') != 'ynab-mirror']
        # The secrets phase is a release that also moves the stack onto the single
        # Transactions secret; the new browser image needs both at once.
        if args.phase == 'secrets':
            secret_arn = aws('secretsmanager', 'describe-secret', '--secret-id', 'Transactions')['ARN']
            client_id = json.loads((ROOT / 'cdk.json').read_text())['context']['adminAuth0ClientId']
            migrate_secrets(template, secret_arn, client_id)
        elif 'ApplicationSecret' in resources:
            raise SystemExit('Run the secrets phase before further releases')

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
    release_allowed = {'gateway', 'processor', 'BrowserTask', 'AdminBranchMain', 'OperatorEmail', 'TaskExecutionRole', 'ProcessorRole', 'GatewayRole', 'PrivateState', 'ScheduleSyncYNAB'}
    allowed = {'release': release_allowed,
               'secrets': release_allowed | {'GatewayRole', 'BrowserRole', 'Admin', *RETIRED_SECRETS},
               'cutover': {'AdminDomain', 'AdminBranch'}}[args.phase]
    if not set(changed) <= allowed:
        raise SystemExit(f'Unexpected resource changes: {changed}')
    path = output / f'{args.phase}-template.json'
    path.write_text(json.dumps(template, indent=2)); path.chmod(0o600)
    template_key = upload(path, prefix)
    url = f'https://{BUCKET}.s3.{REGION}.amazonaws.com/{template_key}'
    metadata = {'revision': revision, 'branch': 'main', 'phase': args.phase, 'changedResources': changed, 'templateUrl': url}
    if args.phase in ('release', 'secrets'):
        metadata.update(lambdaCodeHash=base64.b64encode(hashlib.sha256(archive.read_bytes()).digest()).decode(), imageDigest=digest)
    (output / f'{args.phase}.json').write_text(json.dumps(metadata, indent=2))
    name = f'main-{args.phase}-{revision[:12]}'
    # NoEcho admin parameters: new values only in the secrets phase, otherwise
    # the values already on the stack. They travel in a private file, never argv.
    stack_parameters = {p['ParameterKey'] for p in aws('cloudformation', 'describe-stacks', '--stack-name', STACK)['Stacks'][0].get('Parameters', [])}
    new_values = admin_parameter_values(args.admin_values_from, args.vault) if args.phase == 'secrets' or args.refresh_admin else {}
    parameters = []
    for key, spec in template.get('Parameters', {}).items():
        if key in new_values:
            parameters.append({'ParameterKey': key, 'ParameterValue': new_values[key]})
        elif key in stack_parameters:
            parameters.append({'ParameterKey': key, 'UsePreviousValue': True})
        elif 'Default' not in spec:
            raise SystemExit(f'No value for parameter {key}')
    request = {'StackName': STACK, 'ChangeSetName': name, 'ChangeSetType': 'UPDATE', 'TemplateURL': url,
               'Capabilities': ['CAPABILITY_IAM', 'CAPABILITY_NAMED_IAM'], 'Parameters': parameters}
    with tempfile.TemporaryDirectory(prefix='transactions-change-set-') as directory:
        request_path = Path(directory) / 'change-set.json'
        fd = os.open(request_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as f:
            json.dump(request, f)
        aws('cloudformation', 'create-change-set', '--cli-input-json', f'file://{request_path}')
    subprocess.run(['aws', '--region', REGION, 'cloudformation', 'wait', 'change-set-create-complete',
                    '--stack-name', STACK, '--change-set-name', name], check=True)
    changes = aws('cloudformation', 'describe-change-set', '--stack-name', STACK, '--change-set-name', name)
    print(json.dumps(metadata, indent=2), flush=True)
    # CloudFormation also lists indirect references to modified resources.
    if any(c['ResourceChange'].get('Replacement') == 'True' and c['ResourceChange']['LogicalResourceId'] not in {'BrowserTask'} for c in changes['Changes']):
        raise SystemExit('Unexpected resource replacement; inspect the prepared change set')
    for change in changes['Changes']:
        c = change['ResourceChange']
        print(c['Action'], c['LogicalResourceId'], c['ResourceType'], 'replacement=' + c.get('Replacement', '-'), flush=True)
    if args.prepare_only:
        print(f'Prepared {name}; execute it after review.', flush=True)
        return
    aws('cloudformation', 'execute-change-set', '--stack-name', STACK, '--change-set-name', name)
    print('Deployment started; verify UPDATE_COMPLETE and the main package hashes.', flush=True)


if __name__ == '__main__':
    main()
