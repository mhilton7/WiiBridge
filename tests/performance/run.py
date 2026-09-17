#!/usr/bin/env python3
"""Run identical synthetic fixtures against a selected WiiBridge checkout."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess as sp
import time

parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('phase', choices=['baseline','optimized'])
parser.add_argument('--repository', type=Path, default=Path.cwd())
parser.add_argument('--output', type=Path, required=True)
args=parser.parse_args()
root=args.repository.resolve()
output=args.output.resolve()
output.mkdir(parents=True,exist_ok=True)
out=output/args.phase
out.mkdir()  # Preserve earlier evidence: use a new output directory to repeat.
phase=args.phase
harness=Path(__file__).resolve().parent/'harness'
replacements={
  'server/host-daemon/vdisk/performance_audit_test.go':'vdisk_audit_test.go',
  'server/host-daemon/vdisk/latency_performance_audit_test.go':'latency_audit_test.go',
  'server/host-daemon/gamecube/performance_audit_test.go':'gamecube_audit_test.go',
  'server/host-daemon/gamecube/startup_audit_test.go':'startup_audit_test.go',
  'server/host-daemon/gamecube/save_performance_audit_test.go':'save_audit_test.go',
  'server/host-daemon/gamecube/legacy_performance_audit_test.go':'legacy_audit_test.go',
  'server/host-daemon/fat32virtual/performance_audit_test.go':'layout_audit_test.go',
  'server/host-daemon/store/performance_audit_test.go':'store_audit_test.go',
  'server/host-daemon/performance_audit_test.go':'control_audit_test.go',
  'tests/nbd/performance_audit_test.go':'nbd_audit_test.go',
  'tests/nbd/nbd_test.go':'nbd_helpers_test.go',
}
overlay_path=out/'overlay.json'
overlay_path.write_text(json.dumps({'Replace':{str(root/path):str(harness/(fixture+'.txt')) for path,fixture in replacements.items()}},indent=2)+'\n')
metadata={'repository_revision':sp.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip(),
          'fixtures':{name:hashlib.sha256((harness/(name+'.txt')).read_bytes()).hexdigest() for name in replacements.values()},
          'gomaxprocs':8,'gogc':100,'go_toolchain':'go1.25.12','samples':6,'benchtime':'750ms',
          'temporary_storage':str(output/'disk-backed-tmp')}
(out/'inputs.json').write_text(json.dumps(metadata,indent=2)+'\n')
os.chdir(root)

temporary=output/'disk-backed-tmp'
temporary.mkdir(exist_ok=True)
env=dict(os.environ,TMPDIR=str(temporary),GOMAXPROCS='8',GOGC='100',GOTOOLCHAIN='go1.25.12',CGO_ENABLED='0',GOCACHE='/tmp/wiibridge-go-cache',GOPATH='/tmp/wiibridge-gopath')
overlay=['-overlay',str(overlay_path)]
groups=[
    ('gc-startup','./server/host-daemon/gamecube','^BenchmarkAuditGameCubeStartup$'),
    ('wii','./server/host-daemon/vdisk','^Benchmark(AuditWii|LargeFAT)'),
    ('gc-managed','./server/host-daemon/gamecube','^Benchmark(AuditGameCubeManaged|AuditSaveRead|SaveOverlayWriteAccounting)$'),
    ('gc-backend','./server/host-daemon/fat32virtual','^Benchmark(AuditGameCubeLayout|SequentialReadsOneISO|RandomReadsOneISO|SwitchingThirtyTwoSources|ExtentLookup10000|ExtractedFSTFiveThousandFiles|ReadCrossingSourceExtents|ConcurrentReads|ReadPathMetrics)$'),
    ('store','./server/host-daemon/store','^BenchmarkAuditReconcile$'),
    ('nbd-tls','./tests/nbd','^BenchmarkAuditNBDTLS$'),
    ('nbd-framing','./server/nbd-plugin','^Benchmark(NBDReadPathMetrics|RequestBuffer64KiB)$'),
    ('dashboard','./server/host-daemon','^Benchmark(AuditDashboard|AuditScan|PerformanceSummarySerialization)$'),
    ('pi','./pi/controller','^Benchmark(CachedPiMetrics|PiMetricsEndpoint)$'),
    ('metrics','./shared/perf','^BenchmarkObserveNBDRead'),
]
records=[]
with (out/'benchmarks.txt').open('w') as aggregate:
    for label,package,pattern in groups:
        command=['go','test',*overlay,'-run','^$','-bench',pattern,'-benchmem','-benchtime=750ms','-count=6',package]
        started=time.monotonic()
        print('MEASURE '+label,flush=True)
        with (out/(label+'-benchmarks.txt')).open('w') as log:
            result=sp.run(command,env=env,stdout=log,stderr=sp.STDOUT)
        text=(out/(label+'-benchmarks.txt')).read_text()
        if result.returncode == 0 and not any(line.startswith('Benchmark') for line in text.splitlines()):
            raise SystemExit('No benchmark matched '+label)
        aggregate.write(text+'\n')
        aggregate.flush()
        record={'name':label,'command':command,'status':'PASS' if result.returncode==0 else 'FAIL','elapsed_seconds':round(time.monotonic()-started,3),'completed_utc':datetime.datetime.now(datetime.timezone.utc).isoformat()}
        records.append(record)
        (out/'measurements.json').write_text(json.dumps(records,indent=2)+'\n')
        if result.returncode:
            print(text[-5000:],flush=True)
            raise SystemExit(result.returncode)
        print('DONE '+label,flush=True)

env['WIIBRIDGE_AUDIT_LATENCY_OUTPUT']=str(out/'wii-latency.json')
with (out/'wii-latency.log').open('w') as log:
    sp.run(['go','test',*overlay,'-run','^TestAuditWiiLatencyDistribution$','-count=1','./server/host-daemon/vdisk'],env=env,stdout=log,stderr=sp.STDOUT,check=True)
with (out/'dashboard-polling.json').open('w') as log:
    command=['node',str(harness.parent/'dashboard-polling.cjs')]
    if phase!='baseline':command.append('--expect-optimized')
    sp.run(command,stdout=log,stderr=sp.STDOUT,check=True)
print('MEASUREMENTS COMPLETE '+phase,flush=True)
