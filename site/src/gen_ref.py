# SPDX-FileCopyrightText: 2026 Agaetis
# SPDX-License-Identifier: Apache-2.0
"""Reference data for the site, read from the repository itself:
the CRDs (config/crd), the helm-docs tables of the chart READMEs and the
REQUIRED markers of values-prod.yaml."""
import glob, json, re, subprocess
import yaml

def _walk(sch, path, req, out):
    for k, v in sch.get('properties', {}).items():
        p = f"{path}.{k}" if path else k
        t = v.get('type', '')
        if t == 'array':
            t = f"[]{v.get('items', {}).get('type', 'object')}"
        if v.get('x-kubernetes-int-or-string'):
            t = 'int|string'
        row = {'p': p, 't': t, 'r': k in req, 'd': re.sub(r'\s+', ' ', v.get('description', '')).strip()}
        if 'enum' in v: row['e'] = v['enum']
        if 'default' in v: row['def'] = v['default']
        for c in ('minimum', 'maximum', 'pattern', 'minLength', 'maxLength', 'minItems', 'maxItems', 'format'):
            if c in v: row.setdefault('c', {})[c] = v[c]
        out.append(row)
        sub = v.get('items', v) if v.get('type') == 'array' else v
        if sub.get('properties'):
            _walk(sub, p + ('[]' if v.get('type') == 'array' else ''), sub.get('required', []), out)

def kinds(root):
    out = {}
    for f in sorted(glob.glob(f'{root}/config/crd/*.yaml')):
        d = yaml.safe_load(open(f)); n = d['spec']['names']; ver = d['spec']['versions'][0]
        s = ver['schema']['openAPIV3Schema']; sp = s['properties'].get('spec', {}); st = s['properties'].get('status', {})
        spec, status = [], []
        _walk(sp, 'spec', sp.get('required', []), spec); _walk(st, 'status', [], status)
        out[n['kind']] = {'scope': d['spec']['scope'], 'short': n.get('shortNames', []), 'cats': n.get('categories', []),
                          'desc': re.sub(r'\s+', ' ', s.get('description', '')).strip(),
                          'cols': [{'n': c['name'], 'j': c['jsonPath']} for c in ver.get('additionalPrinterColumns', [])],
                          'spec': spec, 'status': status}
    return out

def _table(md):
    s = open(md).read(); i = s.index('## Values'); rows = []
    for l in s[i:].splitlines():
        if l.startswith('| ') and not l.startswith('| Key'):
            c = [x.strip() for x in re.split(r'(?<!\\)\|', l)[1:-1]]
            if len(c) == 4: rows.append({'k': c[0], 't': c[1], 'def': c[2].strip('`'), 'd': c[3]})
    return rows

def _required(f):
    out, stack = [], []
    for l in open(f):
        m = re.match(r'^(\s*)([A-Za-z0-9_.-]+):', l)
        if m:
            ind = len(m.group(1))
            while stack and stack[-1][0] >= ind: stack.pop()
            stack.append((ind, m.group(2)))
            if 'REQUIRED' in l: out.append('.'.join(k for _, k in stack))
    return out

def reference(root):
    commit = subprocess.run(['git', '-C', root, 'log', '-1', '--format=%h|%cs'], capture_output=True, text=True).stdout.strip()
    return {'kinds': kinds(root),
            'values': {'takecare': _table(f'{root}/charts/takecare/README.md'),
                       'takecare-crds': _table(f'{root}/charts/takecare-crds/README.md'),
                       'required': _required(f'{root}/charts/takecare/values-prod.yaml')},
            'commit': commit}
