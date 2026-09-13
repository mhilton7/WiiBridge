# Full-system performance audit fixtures

These fixtures contain synthetic data only. They exercise Wii catalog scaling,
both FAT copies, GameCube generation validation and construction, clean and
mixed save reads, durable save writes, SQLite reconciliation, real loopback
mutual-TLS NBD, dashboard rendering, scanning, and metrics.

Run the normal validation before collecting a baseline. Use the same Go
toolchain, machine, filesystem, and fixtures for both revisions. Keep firmware
builds and other heavy work out of measurement runs.

```sh
python3 tests/performance/run.py baseline \
  --repository /path/to/pinned-baseline --output /path/to/ext4-results
python3 tests/performance/run.py optimized \
  --repository /path/to/optimized-checkout --output /path/to/ext4-results
benchstat /path/to/ext4-results/baseline/benchmarks.txt \
  /path/to/ext4-results/optimized/benchmarks.txt
```

The output directory must be on the filesystem being measured. In particular,
do not accidentally use a tmpfs temporary directory for durable-write results.
The runner records the revision and fixture hashes, selects Go 1.25.12,
GOMAXPROCS=8 and GOGC=100, and collects six 750 ms samples per benchmark. It
refuses to overwrite an existing phase. Node.js is required for the browser
polling check. Benchmark source templates use a Go overlay so exactly the same
workload can run against the unmodified baseline. The NBD helper overlay only
generalizes existing test helpers to testing.TB.

The latency fixture writes and syncs allocated source files. Warm samples use
the Linux page cache. Evicted samples use POSIX_FADV_DONTNEED and verify the
queried source pages are absent with mincore before each read. Device,
hypervisor, storage-controller and NAS caches remain uncontrolled. No global
cache eviction is performed. The loopback TLS benchmark counts encrypted
transport bytes and writes, excluding TCP/IP overhead; it does not model Wi-Fi.

For syscall counts, use the generated overlay to compile the GameCube test
binary with `go test -c`. Run `TestAuditSaveReadSyscalls/mixed=false` and
`TestAuditSaveReadSyscalls/mixed=true` under `strace -f -c -e trace=pread64`.
The fixed workload performs 100 unaligned 1 MiB reads. Also run the binary with
`-test.run=^$` to measure and subtract process-startup preads. Timing under
ptrace is not a throughput measurement.

The optional legacy-generation test takes WIIBRIDGE_AUDIT_LEGACY_ROOT. Create
it once with the baseline and WIIBRIDGE_AUDIT_CREATE_LEGACY=1, then omit that
flag when opening it on the optimized revision. It checks unchanged metadata
hashes and independently compares source bytes.

After building the OCI image, exercise real read-only mounts and mTLS:

```sh
AUDIT_PHASE=optimized AUDIT_CASE=wii-interop AUDIT_KERNEL=1 \
  python3 tests/performance/container-interop.py
AUDIT_PHASE=optimized AUDIT_CASE=gc-interop AUDIT_PLATFORM=gamecube AUDIT_KERNEL=1 \
  python3 tests/performance/container-interop.py
sudo python3 tests/truenas/separate-libraries-test.py
python3 tests/truenas/compose-separate-parser-test.py
```

The optional kernel test requires Docker, skopeo, OpenSSL, libnbd tools,
nbd-client, dosfstools and passwordless sudo. It uses an unused local NBD
device, checks the read-only flag, mounts read-only, compares complete synthetic
payload hashes, rejects a block write, and runs fsck without repairs. It cleans
up only the device it connected and unloads NBD only if it loaded the module.
Omit AUDIT_KERNEL to run the container and userspace protocol checks alone.
Use a new AUDIT_CASE to repeat without replacing prior evidence.

Raw local output can include machine details and temporary paths. Sanitize it
before publication; never publish fixture private keys or operator credentials.
Physical Pi boot, USB gadget enumeration, Wii launches and TrueNAS/ZFS latency
still require a separate hardware qualification run.
