#!/usr/bin/env python3
"""Restore the preserved step-view UI; leave overview and database untouched."""
from pathlib import Path
import hashlib
import json
import shutil

root = Path(__file__).resolve().parent
snapshot = root / 'benchmarks' / 'step-view-v1'
manifest = json.loads((snapshot / 'manifest.json').read_text())
for name in ('index.html', 'app.js', 'styles.css'):
    if hashlib.sha256((snapshot / name).read_bytes()).hexdigest() != manifest[name]:
        raise SystemExit('Snapshot integrity check failed: ' + name)
for name in ('index.html', 'app.js', 'styles.css'):
    shutil.copy2(snapshot / name, root / name)
print('Benchmark restored. Refresh http://127.0.0.1:8766/')
