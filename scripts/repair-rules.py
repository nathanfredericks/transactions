#!/usr/bin/env python3
"""Prepare or apply subscription repairs; back up records and validate YNAB references.
Only DynamoDB overrides are written. Historical transactions are never modified.
"""
import argparse, datetime, json, os, pathlib, subprocess, urllib.request, uuid
REGION='ca-central-1'
TABLE='TransactionOverrides'
BUDGET='e0e7f122-6f2f-41f3-9b84-6d8f49fd5eab'
SECRET='arn:aws:secretsmanager:ca-central-1:187489282488:secret:transactions-ZuMkXL'
def aws(*args):
    return json.loads(subprocess.check_output(['aws',*args,'--region',REGION,'--output','json']) or '{}')
def main():
    p=argparse.ArgumentParser();p.add_argument('--apply',action='store_true');p.add_argument('--backup-root',default=str(pathlib.Path.home()/'.codex/backups/transactions'));a=p.parse_args()
    stamp=datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    backup=pathlib.Path(a.backup_root)/stamp;backup.mkdir(parents=True,mode=0o700)
    before=aws('dynamodb','scan','--table-name',TABLE)
    def save(name,value):
        path=backup/name;path.write_text(json.dumps(value,indent=2)+'\n');path.chmod(0o600);return path
    save('overrides-before.json',before)
    token=json.loads(aws('secretsmanager','get-secret-value','--secret-id',SECRET)['SecretString'])['YNAB_ACCESS_TOKEN']
    def ynab(resource):
        req=urllib.request.Request(f'https://api.youneedabudget.com/v1/budgets/{BUDGET}/{resource}',headers={'Authorization':f'Bearer {token}'})
        with urllib.request.urlopen(req) as r:return json.load(r)['data']
    payees={x['name']:x['id'] for x in ynab('payees')['payees'] if not x['deleted'] and not x.get('transfer_account_id')}
    categories={x['name']:x['id'] for g in ynab('categories')['category_groups'] if not g['deleted'] for x in g['categories'] if not x['deleted']}
    live=before['Items'];existing={x['name']['S']:x for x in live}
    payee_ids=set(payees.values());category_ids=set(categories.values())
    for x in live:
        name=x['name']['S'];query=json.loads(x['query']['S']);assert isinstance(query,dict),f'Invalid query: {name}'
        assert x['payee']['S'] in payee_ids,f'Inactive payee: {name}'
        if x.get('category',{}).get('S'):assert x['category']['S'] in category_ids,f'Inactive category: {name}'
    specs=json.loads((pathlib.Path(__file__).resolve().parent.parent/'testdata/subscriptions.json').read_text())
    planned=[]
    for spec in specs:
        prior=existing.get(spec['name']) or (existing.get('iCloud+') if spec['name']=='iCloud+ with 200 GB storage' else None)
        item=dict(prior) if prior else {'id':{'S':str(uuid.uuid4())},'createdAt':{'S':datetime.datetime.now(datetime.timezone.utc).isoformat()}}
        for k,v in {'name':spec['name'],'payee':payees[spec['payeeName']],'category':categories[spec['categoryName']],'memo':spec['memo'],'query':json.dumps(spec['query'],separators=(',',':')),'updatedAt':datetime.datetime.now(datetime.timezone.utc).isoformat()}.items():item[k]={'S':v}
        planned.append((prior,item))
    byid={x['id']['S']:x for x in live}
    for _,x in planned:byid[x['id']['S']]=x
    candidate=save('overrides-candidate.json',{'Items':list(byid.values())})
    repo=pathlib.Path(__file__).resolve().parent.parent
    subprocess.run(['go','run','./cmd/audit-rules','-rules',str(candidate)],cwd=repo,check=True)
    save('repair-plan.json',{'existingValidated':len(live),'subscriptions':len(specs),'writes':[x for _,x in planned],'applied':a.apply})
    if a.apply:
        for prior,item in planned:
            args=['dynamodb','put-item','--table-name',TABLE,'--item',json.dumps(item)]
            if prior:
                args+=['--condition-expression','updatedAt = :previous','--expression-attribute-values',json.dumps({':previous':prior['updatedAt']})]
            else:args+=['--condition-expression','attribute_not_exists(id)']
            aws(*args)
        after=save('overrides-after.json',aws('dynamodb','scan','--table-name',TABLE))
        subprocess.run(['go','run','./cmd/audit-rules','-rules',str(after)],cwd=repo,check=True)
    print(f"{'Applied' if a.apply else 'Prepared'} {len(specs)} subscription rules; audited {len(live)} existing records. Backup: {backup}")
if __name__=='__main__':main()
