#!/usr/bin/env python3
"""One-off: build the single Transactions secret from the old per-purpose secrets.

Reads transactions-engine/application, the three bank secrets,
cloakbrowser-license and browser-broker in memory and creates the secret
"Transactions". The old secrets are left untouched for rollback. Only key names
and checks are printed, never values. Rerunning with --update replaces the
current value of an existing Transactions secret instead of creating it.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

REGION = 'ca-central-1'
NAME = 'Transactions'
BANKS = ['eq-bank', 'nbdb', 'rogers-bank']
APPLICATION_KEYS = ['WEBHOOK_TOKEN', 'YNAB_ACCESS_TOKEN', 'OPENAI_API_KEY', 'PUSHOVER_TOKEN', 'PUSHOVER_USER']


def aws(*args):
    out = subprocess.run(['aws', '--region', REGION, *args], capture_output=True, text=True, check=True).stdout
    return json.loads(out) if out.strip() else {}


def aws_with_input(*args, request):
    # The request holds secret values, so it goes through a private file,
    # never through process arguments.
    with tempfile.TemporaryDirectory(prefix='transactions-merge-') as directory:
        path = Path(directory) / 'request.json'
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as f:
            json.dump(request, f)
        return aws(*args, '--cli-input-json', f'file://{path}')


def value(secret_id):
    return aws('secretsmanager', 'get-secret-value', '--secret-id', secret_id)['SecretString']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--update', action='store_true')
    args = parser.parse_args()

    application = json.loads(value('transactions-engine/application'))
    missing = [k for k in APPLICATION_KEYS if not application.get(k)]
    if missing:
        raise SystemExit(f'application secret is missing {missing}')
    merged = dict(application)
    merged['CLOAKBROWSER_LICENSE_KEY'] = value('transactions-engine/cloakbrowser-license').strip()
    merged['BROWSER_BROKER_TOKEN'] = value('transactions-engine/browser-broker').strip()
    for key in ['CLOAKBROWSER_LICENSE_KEY', 'BROWSER_BROKER_TOKEN']:
        if not merged[key] or merged[key].startswith('{'):
            raise SystemExit(f'{key} source is empty or JSON, expected a plain string')
    merged['BANKS'] = {}
    for bank in BANKS:
        login = json.loads(value(f'transactions-engine/{bank}'))
        entry = {k: login.get(k, '') for k in ['username', 'password', 'mailToken']}
        empty = [k for k, v in entry.items() if not v]
        if empty:
            raise SystemExit(f'{bank} secret has empty {empty}')
        extra = sorted(set(login) - set(entry))
        if extra:
            raise SystemExit(f'{bank} secret has unexpected keys {extra}; not merging')
        merged['BANKS'][bank] = entry

    request = {'SecretString': json.dumps(merged)}
    if args.update:
        request['SecretId'] = NAME
        result = aws_with_input('secretsmanager', 'put-secret-value', request=request)
    else:
        request.update(Name=NAME, Description='Transactions Engine runtime secrets: application keys, browser license and broker token, bank logins')
        result = aws_with_input('secretsmanager', 'create-secret', request=request)

    stored = json.loads(value(NAME))
    if stored != merged:
        raise SystemExit('stored value differs from the merged value')
    print('ARN', result['ARN'])
    print('keys', sorted(k for k in stored if k != 'BANKS'), 'banks', sorted(stored['BANKS']))


if __name__ == '__main__':
    main()
