#!/usr/bin/env python3
"""Run the production browser locally; validate and save auth without importing."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('bank', help='Registered bank ID')
parser.add_argument('--mode', choices=['login', 'api', 'renew'], default='login')
parser.add_argument('--region', default='ca-central-1')
parser.add_argument('--stack', default='TransactionsEngine')
parser.add_argument('--image', default='transactions-engine-browser:verify')
args = parser.parse_args()
root = Path(__file__).resolve().parent.parent

def aws(*command):
    return json.loads(subprocess.check_output(['aws', '--region', args.region, *command], text=True))

# Only resource metadata is inspected. Secret values never enter command arguments.
resources = aws('cloudformation', 'list-stack-resources', '--stack-name', args.stack)['StackResourceSummaries']
def resource(kind):
    matches = [r['PhysicalResourceId'] for r in resources if r['ResourceType'] == kind]
    if len(matches) != 1:
        raise SystemExit(f'Expected exactly one {kind} in {args.stack}')
    return matches[0]

env = dict(os.environ, GOOS='linux', GOARCH='arm64', CGO_ENABLED='0')
subprocess.run(['go', 'build', '-o', 'dist/bank-browser', './cmd/browser'], cwd=root, env=env, check=True)
credentials = aws('configure', 'export-credentials', '--format', 'process')
values = {
    'AWS_REGION': args.region,
    'TZ': 'America/Halifax',
    'AWS_ACCESS_KEY_ID': credentials['AccessKeyId'],
    'AWS_SECRET_ACCESS_KEY': credentials['SecretAccessKey'],
    'STATE_TABLE': resource('AWS::DynamoDB::Table'),
    'STATE_BUCKET': resource('AWS::S3::Bucket'),
    'SETTINGS_PARAMETER': '/transactions-engine/settings',
}
if credentials.get('SessionToken'):
    values['AWS_SESSION_TOKEN'] = credentials['SessionToken']
# NamedTemporaryFile is private (0600) and removed even if Docker fails.
with tempfile.NamedTemporaryFile(mode='w', prefix='bank-verify-', suffix='.env') as private:
    for name, value in values.items():
        if '\n' in value or '\r' in value:
            raise SystemExit('Invalid environment value')
        private.write(f'{name}={value}\n')
    private.flush()
    command = ['docker', 'run', '--rm', '--init', '--env-file', private.name,
        '--mount', f'type=bind,src={root / "dist/bank-browser"},dst=/usr/local/bin/bank-browser,readonly']
    if args.mode != 'login':
        command += ['--entrypoint', '/usr/local/bin/bank-browser']
    flag = {'login': '--verify-bank', 'api': '--verify-api', 'renew': '--verify-renew'}[args.mode]
    result = subprocess.run(command + [args.image, flag, args.bank], cwd=root)
    raise SystemExit(result.returncode)
