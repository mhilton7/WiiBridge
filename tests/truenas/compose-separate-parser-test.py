#!/usr/bin/env python3
"""Check separate-library Compose interpolation with no legacy-path variable."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

compose = Path(sys.argv[1] if len(sys.argv) > 1 else 'deploy/truenas/compose.separate-libraries.yaml').resolve()
env = {k: v for k, v in os.environ.items() if not k.startswith(('WIIBRIDGE_', 'COMPOSE_'))}
env.update(WIIBRIDGE_IMAGE='ghcr.io/mhilton7/wiibridge-host@sha256:' + '1' * 64,
           WIIBRIDGE_UID='568', WIIBRIDGE_GID='568',
           WIIBRIDGE_ADMIN_TOKEN='synthetic-compose-parser-only',
           WIIBRIDGE_HTTPS_BIND='127.0.0.1', WIIBRIDGE_NBD_BIND='127.0.0.1',
           WIIBRIDGE_WII_LIBRARY_PATH='/synthetic/wii',
           WIIBRIDGE_GAMECUBE_LIBRARY_PATH='/synthetic/gamecube')
for name in ('CONFIG', 'DATA', 'CERTS', 'LOGS', 'BACKUPS'):
    env['WIIBRIDGE_' + name + '_PATH'] = '/synthetic/' + name.lower()
with tempfile.TemporaryDirectory(prefix='wiibridge-compose-parser-') as root:
    empty = Path(root) / 'empty.env'
    empty.write_text('')
    command = ['docker', 'compose', '--env-file', str(empty), '-f', str(compose), 'config', '--format', 'json']
    result = subprocess.run(command, env=env, text=True, capture_output=True)
    assert result.returncode == 0, result.stderr
    service = json.loads(result.stdout)['services']['wiibridge-host']
    mounts = {v['target']: v for v in service['volumes']}
    for name in ('wii', 'gamecube'):
        mount = mounts['/library/' + name]
        assert mount['source'] == '/synthetic/' + name
        assert mount['type'] == 'bind' and mount['read_only'] is True
        assert mount['bind'].get('create_host_path', False) is False
    assert len(mounts) == 7
    for missing in ('WIIBRIDGE_WII_LIBRARY_PATH', 'WIIBRIDGE_GAMECUBE_LIBRARY_PATH'):
        for value in (None, ''):
            case = dict(env)
            if value is None:
                del case[missing]
            else:
                case[missing] = value
            # An old shared-path setting must not conceal a missing dedicated path.
            case['WIIBRIDGE_LIBRARY_PATH'] = '/synthetic/legacy'
            result = subprocess.run(command, env=case, text=True, capture_output=True)
            assert result.returncode != 0 and missing in result.stderr, (missing, value, result.stderr)
print('separate-library Compose parser: PASS (distinct paths, readonly mounts, missing/empty paths rejected)')
