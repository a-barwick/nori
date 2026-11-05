#!/usr/bin/env python3
"""Verify retained results from a provider smoke without making another inference request."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--emulator', required=True)
p.add_argument('--database', required=True)
p.add_argument('--source-revision', required=True, help='Revision used by the original paid smoke')
p.add_argument('--log', type=Path, required=True)
p.add_argument('--output', type=Path, required=True)
a = p.parse_args()
if not re.fullmatch(r'127\.0\.0\.1:[0-9]{1,5}', a.emulator) or not re.fullmatch(r'nori-shared-[a-f0-9]+', a.database):
    p.error('Only isolated shared-service emulator records are accepted')
# Recovery/UI changes may be checked deterministically. A provider implementation
# change invalidates this reuse and requires fresh authorized provider evidence.
provider_paths = ['core/internal/conversation/openai.go', 'core/internal/conversation/model.go',
                  'core/internal/agent/openrouter', 'core/internal/spend', 'core/internal/task']
subprocess.run(['git', 'diff', '--exit-code', a.source_revision, '--', *provider_paths], cwd=ROOT, check=True)
base = f'http://{a.emulator}/v1/projects/bot-111549/databases/{a.database}/documents/owners/austinbarwickdavis@gmail.com'
def decode(value):
    if 'mapValue' in value:
        return {key: decode(item) for key, item in value['mapValue'].get('fields', {}).items()}
    if 'arrayValue' in value:
        return [decode(item) for item in value['arrayValue'].get('values', [])]
    if 'integerValue' in value:
        return int(value['integerValue'])
    return next(iter(value.values()))
def collection(path):
    with urllib.request.urlopen(base + '/' + path + '?pageSize=1000', timeout=10) as response:
        data = json.load(response)
    assert not data.get('nextPageToken'), 'Evidence collection was truncated'
    return [decode({'mapValue': doc}) for doc in data.get('documents', [])]
log = a.log.read_text()
assert '(pass) selected Sol streams a saved answer' in log, 'Original observed stream assertion is missing'
chats = collection('chats')
chat = next(c for c in chats if c['title'] == 'Provider smoke')
reply = chat['messages'][-1]
assert reply['status'] == 'completed' and '42' in reply['text']
assert any(op['name'] == 'upload_read' and op['outcome'] == 'completed' for op in reply['operations'])
assert chat['usage'] and all(u['model'] == 'gpt-6.1-sol' and u['request_status'] == 'received' and u['cost_status'] == 'calculated' for u in chat['usage'])
runs = collection('runs')
assert len(runs) == 1 and runs[0]['status'] == 'cancelled'
steps = collection('runs/' + runs[0]['id'] + '/steps')
step = next(s for s in steps if s.get('model') == 'openai/gpt-6.1-terra' and s['status'] == 'completed')
assert any(c['name'] == 'request_input' and 'column' in c['arguments'].lower() for c in step['calls'])
assert step['cost_status'] == 'reported' and step['cost_usd'] > 0
spec = importlib.util.spec_from_file_location('migration_verify', ROOT / 'migration/verify.py')
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)
report = {'mode': 'provider', 'status': 'passed', 'migration_ready': False, 'acceptance': ['provider-smoke'],
          'scope': 'Retained results from the original paid smoke; no repeated paid calls',
          'source_revision': a.source_revision, 'source_hashes': verify.source_hashes(),
          'plan_sha256': hashlib.sha256((ROOT / 'migration/plan.json').read_bytes()).hexdigest(),
          'original_log': str(a.log), 'original_log_sha256': hashlib.sha256(a.log.read_bytes()).hexdigest(),
          'evidence_reader_repair': 'Inner model usage is stored in task steps, not the outer session usage projection.',
          'checks': [{'id': name, 'status': 'passed'} for name in ['Sol streamed CSV reply', 'Terra request_input', 'Known provider costs', 'Smoke task cancelled', 'Provider implementation unchanged']],
          'chat': chat, 'run': runs[0], 'steps': steps}
a.output.parent.mkdir(parents=True, exist_ok=True)
a.output.write_text(json.dumps(report, indent=2) + '\n')
print(json.dumps({'status': report['status'], 'report': str(a.output)}))
