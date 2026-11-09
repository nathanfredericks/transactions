#!/usr/bin/env python3
"""Show or deploy the CDK stack from committed, pushed main source.

`diff` prints what would change. `deploy` prints the same diff, asks for
confirmation, logs Docker in to the CDK asset registry, then runs `cdk deploy`,
which builds the Lambda package and the browser image from this checkout.
"""
import argparse
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
CDK = ['npx', '--yes', 'aws-cdk@2.1144.0']
STACK = 'TransactionsEngine'
REGION = 'ca-central-1'
REGISTRY = '187489282488.dkr.ecr.ca-central-1.amazonaws.com'


def git(*args):
    return subprocess.check_output(['git', *args], cwd=ROOT, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('phase', choices=['diff', 'deploy'])
    args = parser.parse_args()
    if git('branch', '--show-current') != 'main':
        raise SystemExit('Deployment requires the main branch')
    if git('status', '--porcelain'):
        raise SystemExit('Commit local changes before deployment')
    revision = git('rev-parse', 'HEAD')
    remote = git('-c', 'credential.helper=', '-c', 'credential.helper=!gh auth git-credential',
                 'ls-remote', 'https://github.com/nathanfredericks/transactions.git', 'refs/heads/main').split()[0]
    if remote != revision:
        raise SystemExit('Push this main revision before deployment')
    license_file = Path(os.environ.get('CLOAKBROWSER_LICENSE_FILE', Path.home() / '.cloakbrowser/license.key'))
    if args.phase == 'deploy' and not license_file.is_file():
        raise SystemExit('The private browser license file is required to build the browser image')
    env = {**os.environ, 'GOWORK': 'off', 'TRANSACTIONS_REVISION': revision}
    subprocess.run([*CDK, 'diff', STACK], cwd=ROOT, env=env, check=True)
    if args.phase == 'diff':
        return
    if input(f'Deploy {revision[:12]} with the changes above? [y/N] ').strip().lower() != 'y':
        raise SystemExit('Not deployed')
    # The browser image builds on the deployed runtime in the CDK asset registry.
    password = subprocess.check_output(['aws', '--region', REGION, 'ecr', 'get-login-password'], text=True)
    subprocess.run(['docker', 'login', '--username', 'AWS', '--password-stdin', REGISTRY],
                   input=password, text=True, check=True, stdout=subprocess.DEVNULL)
    subprocess.run([*CDK, 'deploy', STACK, '--require-approval', 'never'], cwd=ROOT, env=env, check=True)


if __name__ == '__main__':
    main()
