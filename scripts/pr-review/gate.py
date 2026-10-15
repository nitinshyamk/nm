#!/usr/bin/env python3
"""nhs-agent:pr-review gate (playbook §4). Cheap, deterministic, no GitHub writes.

  gate.py <scope>            print {"work": [...], "followup_at": ms|null, "seeded": bool}; writes <scope>.next.json
  gate.py <scope> --commit   promote <scope>.next.json to <scope>.json (run after the work is handled)

Scopes: hadrian, sidebar, turbo-goblin, gdt, other. State lives in /root/workspace/pr-review/state and is
rebuildable from GitHub; a missing state file seeds silently and only flags unanswered nhs-agent asks.
"""
import hashlib, json, os, subprocess, sys, time

STATE = '/root/workspace/pr-review/state'
FOUR = ['hadrian', 'Sidebar', 'turbo-goblin', 'gdt-ontology-builder']
SCOPES = {'hadrian': 'repo:Hadrian-MTV/hadrian', 'sidebar': 'repo:Hadrian-MTV/Sidebar',
          'turbo-goblin': 'repo:Hadrian-MTV/turbo-goblin', 'gdt': 'repo:Hadrian-MTV/gdt-ontology-builder',
          'other': 'org:Hadrian-MTV ' + ' '.join(f'-repo:Hadrian-MTV/{r}' for r in FOUR)}
EXCLUDE = {'Hadrian-MTV/Sidebar': set(range(5315, 5338))}
LADDER_MIN = [2, 5, 10, 20, 40]
MARK = '**nhs-agent:pr-review**'
AGENT_PREFIXES = (MARK, 'nm-agent:', 'nm:', '(Claude, coordinator.)')


def gh(path, *args):
    out = subprocess.run(['gh', 'api', path, *args], capture_output=True, text=True, check=True).stdout
    return json.loads(out)


def is_agent(body):
    return (body or '').lstrip().startswith(AGENT_PREFIXES)


def unanswered_nhs(ic, rc):
    """Comments addressed to nhs-agent with no agent reply after them in the same thread (C4)."""
    n = 0
    threads = {}
    for c in rc:
        threads.setdefault(c.get('in_reply_to_id') or c['id'], []).append(c)
    for t in threads.values():
        t.sort(key=lambda c: c['created_at'])
        for i, c in enumerate(t):
            if 'nhs-agent' in (c['body'] or '').lower() and not is_agent(c['body']):
                if not any(is_agent(d['body']) for d in t[i + 1:]):
                    n += 1
    ic = sorted(ic, key=lambda c: c['created_at'])
    for i, c in enumerate(ic):
        if 'nhs-agent' in (c['body'] or '').lower() and not is_agent(c['body']):
            if not any(is_agent(d['body']) for d in ic[i + 1:]):
                n += 1
    return n


def main():
    scope = sys.argv[1]
    os.makedirs(STATE, exist_ok=True)
    cur, nxt = f'{STATE}/{scope}.json', f'{STATE}/{scope}.next.json'
    if '--commit' in sys.argv:
        os.replace(nxt, cur)
        return
    seeded = not os.path.exists(cur)
    old = {} if seeded else json.load(open(cur))
    items = gh('search/issues', '-X', 'GET', '-f',
               f'q=is:pr is:open author:nitin-hadrian {SCOPES[scope]}', '-f', 'per_page=100')['items']
    now = int(time.time() * 1000)
    new, work, due = {}, [], []
    for it in items:
        repo, n = it['repository_url'].split('/repos/')[1], it['number']
        if n in EXCLUDE.get(repo, ()):
            continue
        pr = gh(f'repos/{repo}/pulls/{n}')
        if 'nhs-agent:ignore' in (pr.get('body') or ''):
            continue
        checks = gh(f'repos/{repo}/commits/{pr["head"]["sha"]}/check-runs?per_page=100')['check_runs']
        ic = gh(f'repos/{repo}/issues/{n}/comments?per_page=100')
        rc = gh(f'repos/{repo}/pulls/{n}/comments?per_page=100')
        rv = [r for r in gh(f'repos/{repo}/pulls/{n}/reviews?per_page=100') if r['state'] != 'PENDING']
        fp = {'head': pr['head']['sha'], 'base': pr['base']['sha'], 'mergeable': pr['mergeable_state'],
              'draft': pr['draft'], 'body': hashlib.sha1((pr.get('body') or '').encode()).hexdigest()[:10],
              'checks': sorted(f'{c["name"]}={c["status"]}/{c["conclusion"]}' for c in checks),
              'ic': max([c['id'] for c in ic], default=0), 'rc': max([c['id'] for c in rc], default=0),
              'rv': max([r['id'] for r in rv], default=0),
              'edits': max([c['updated_at'] for c in ic + rc], default='')}
        key = f'{repo}#{n}'
        prev = old.get(key)
        changed = [] if prev is None else [k for k in fp if prev.get(k) != fp[k]]
        pending = unanswered_nhs(ic, rc)
        meta = {k: prev[k] for k in ('fix_attempts', 'notified_sha') if prev and k in prev}
        if (prev is None and not seeded) or changed:
            ladder = 0
        elif prev is not None and prev.get('ladder') is not None:
            ladder = prev['ladder'] + 1 if prev['ladder'] + 1 < len(LADDER_MIN) else None
        else:
            ladder = None
        ci_pending = any(c['status'] != 'completed' for c in checks)
        if ci_pending and ladder is not None:
            ladder = min(ladder, 1)  # hold at 5 min while CI runs
        new[key] = {**fp, **meta, 'ladder': ladder}
        if ladder is not None:
            due.append(now + LADDER_MIN[ladder] * 60_000)
        if (seeded and pending) or (not seeded and (prev is None or changed)):
            work.append({'pr': key, 'url': pr['html_url'], 'is_new': prev is None, 'changed': changed,
                         'unanswered_nhs_agent': pending, 'mergeable': fp['mergeable'],
                         'failing_checks': [c for c in fp['checks'] if c.endswith(('/failure', '/timed_out'))]})
    json.dump(new, open(nxt, 'w'), indent=1)
    print(json.dumps({'scope': scope, 'seeded': seeded, 'tracked': len(new), 'work': work,
                      'followup_at': min(due) if due else None}))


if __name__ == '__main__':
    main()
