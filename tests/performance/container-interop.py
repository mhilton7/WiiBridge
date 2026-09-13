#!/usr/bin/env python3
"""Exercise the built OCI with synthetic storage, real read-only mounts and mTLS."""
import hashlib
import fcntl
import json
import os
from pathlib import Path
import socket
import ssl
import struct
import subprocess as sp
import time
import urllib.request

repo = Path.cwd()
out = repo / 'build/perf-audit' / os.environ.get('AUDIT_PHASE', 'baseline') / os.environ.get('AUDIT_CASE', 'container')
out.mkdir(parents=True)  # Refuse to replace existing test evidence.
root = out / 'fixture'
for name in ('library', 'config', 'data', 'certs', 'client', 'logs', 'backups'):
    (root / name).mkdir(parents=True, exist_ok=True)
    (root / name).chmod(0o777 if name in ('data', 'config', 'logs', 'backups') else 0o755)
log = (out / 'run.log').open('w')
def run(args, **kwargs):
    return sp.run(args, check=True, stdout=log, stderr=log, **kwargs)
def capture(args):
    return sp.check_output(args, text=True).strip()
name = 'wiibridge-perf-' + str(os.getpid())
image = name + ':local'
run(['sudo', '-n', 'skopeo', 'copy', 'oci:'+str(repo / 'dist/wiibridge-host-0.1.0-rc.1.oci'), 'docker-daemon:'+image])
run(['go', 'run', './scripts/synthetic-wbfs', str(root / 'library/TEST03.wbfs'), 'TEST03'])
platform = os.environ.get('AUDIT_PLATFORM', 'wii')
if platform == 'gamecube':
    disc = bytearray(2<<20)
    disc[:6] = b'GAUD01'
    struct.pack_into('>I',disc,0x1c,0xc2339f3d)
    disc[0x20:0x20+14] = b'Audit GameCube'
    struct.pack_into('>IIII',disc,0x420,0x2000,0x1000,12,12)
    disc[0x1000]=1
    struct.pack_into('>I',disc,0x1008,1)
    (root/'library/GAUD01.iso').write_bytes(disc)
def cert(args):
    run(['openssl'] + args)
cert(['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=interop-ca', '-addext', 'keyUsage=critical,keyCertSign,cRLSign', '-keyout', str(root/'ca.key'), '-out', str(root/'certs/ca.crt')])
for item, cn, ext, keypath, crtpath in [
    ('server', 'localhost', 'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n', root/'certs/server.key', root/'certs/server.crt'),
    ('client', 'pi-interop', 'extendedKeyUsage=clientAuth\n', root/'client/client-key.pem', root/'client/client-cert.pem')]:
    (root/(item+'.ext')).write_text(ext)
    cert(['req', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN='+cn, '-keyout', str(keypath), '-out', str(root/(item+'.csr'))])
    cert(['x509', '-req', '-days', '2', '-in', str(root/(item+'.csr')), '-CA', str(root/'certs/ca.crt'), '-CAkey', str(root/'ca.key'), '-CAcreateserial', '-extfile', str(root/(item+'.ext')), '-out', str(crtpath)])
    keypath.chmod(0o644 if item == 'server' else 0o600)
for target in ('certs/clients-ca.crt', 'client/ca-cert.pem'):
    (root/target).write_bytes((root/'certs/ca.crt').read_bytes())
(root/'ca.key').unlink()
env = root/'container.env'
env.write_text('WIIBRIDGE_ADMIN_TOKEN='+('synthetic-interop-'*3)+'\n')
env.chmod(0o600)
args = ['sudo', '-n', 'docker', 'run', '-d', '--name', name, '--user', '568:568', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--pids-limit', '256', '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=32m', '--env-file', str(env), '-p', '127.0.0.1::10809', '-p', '127.0.0.1::8445']
for folder in ('library', 'config', 'data', 'certs', 'logs', 'backups'):
    args += ['--mount', f'type=bind,source={root/folder},target=/{folder}' + (',readonly' if folder in ('library','certs') else '')]
args.append(image)
source = root / 'library' / ('GAUD01.iso' if platform == 'gamecube' else 'TEST03.wbfs')
before = hashlib.sha256(source.read_bytes()).hexdigest()
started = False
result = {'status':'FAIL', 'source_sha256':before}
try:
    run(args)
    started = True
    for attempt in range(60):
        rc = sp.run(['sudo', '-n', 'docker', 'exec', name, '/wiibridge-host', 'healthcheck'], stdout=log, stderr=log).returncode
        if rc == 0:
            break
        time.sleep(0.2)
    else:
        raise RuntimeError('container did not become healthy')
    port = capture(['sudo', '-n', 'docker', 'port', name, '10809/tcp']).rsplit(':',1)[1]
    https_port = capture(['sudo','-n','docker','port',name,'8445/tcp']).rsplit(':',1)[1]
    https_context = ssl.create_default_context(cafile=str(root/'certs/ca.crt'))
    def api(path, post=False):
        req = urllib.request.Request('https://localhost:'+https_port+path, data=b'' if post else None, headers={'Authorization':'Bearer '+('synthetic-interop-'*3), 'Accept':'application/json','X-WiiBridge-CSRF':'1'})
        with urllib.request.urlopen(req,context=https_context,timeout=30) as response:
            return json.load(response)
    if platform == 'gamecube':
        for attempt in range(60):
            if api('/api/v1/gamecube').get('games'):
                break
            time.sleep(0.1)
        api('/api/v1/gamecube/library/build',post=True)
        for attempt in range(120):
            state=api('/api/v1/gamecube/library')
            if state['ready']:
                break
            if state['progress']['state'] == 'Failed':
                raise RuntimeError('synthetic GameCube build failed')
            time.sleep(0.1)
        else:
            raise RuntimeError('synthetic GameCube build timed out')
        api('/api/v1/export/gamecube',post=True)
    uri = f'nbds://pi-interop@localhost:{port}/all?tls-certificates={root/"client"}'
    run(['nbdinfo', uri])
    run(['nbdinfo', '--is', 'read-only', uri])
    if sp.run(['nbdinfo', f'nbd://localhost:{port}/all'], stdout=log, stderr=log).returncode == 0:
        raise RuntimeError('plaintext NBD unexpectedly succeeded')
    def readfull(conn, size):
        data = bytearray()
        while len(data) < size:
            part = conn.recv(size-len(data))
            if not part:
                raise RuntimeError('short NBD response')
            data.extend(part)
        return bytes(data)
    conn = socket.create_connection(('127.0.0.1',int(port)), timeout=10)
    magic, opts, flags = struct.unpack('>QQH', readfull(conn,18))
    assert magic == 0x4e42444d41474943 and opts == 0x49484156454f5054
    conn.sendall(struct.pack('>I',3))
    conn.sendall(struct.pack('>QII',opts,5,0))
    assert struct.unpack('>QIII',readfull(conn,20))[1:] == (5,1,0)
    ctx = ssl.create_default_context(cafile=str(root/'certs/ca.crt'))
    ctx.load_cert_chain(str(root/'client/client-cert.pem'),str(root/'client/client-key.pem'))
    conn = ctx.wrap_socket(conn,server_hostname='localhost')
    conn.sendall(struct.pack('>QII',opts,1,3)+b'all')
    size, exportflags = struct.unpack('>QH',readfull(conn,10))
    assert exportflags & 2
    request = 0
    def readat(offset, length):
        global request
        request += 1
        conn.sendall(struct.pack('>IHHQQI',0x25609513,0,0,request,offset,length))
        replymagic,error,handle = struct.unpack('>IIQ',readfull(conn,16))
        assert replymagic == 0x67446698 and error == 0 and handle == request
        return readfull(conn,length)
    mbr = readat(0,512)
    assert mbr[510:] == b'\x55\xaa'
    partition = struct.unpack_from('<I',mbr,454)[0]*512
    boot = readat(partition,512)
    assert boot[510:] == b'\x55\xaa'
    reserved = struct.unpack_from('<H',boot,14)[0]
    fatsectors = struct.unpack_from('<I',boot,36)[0]
    fat1 = partition+reserved*512
    fat2 = fat1+fatsectors*512
    for relative in range(0,fatsectors*512,65536):
        length = min(65536,fatsectors*512-relative)
        assert readat(fat1+relative,length) == readat(fat2+relative,length)
    conn.sendall(struct.pack('>IHHQQI',0x25609513,0,2,request+1,0,0))
    conn.close()
    if os.environ.get('AUDIT_KERNEL') == '1':
        # This lock and ownership checks protect other local NBD users.
        lock = (repo/'build/perf-audit/nbd.lock').open('w')
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        loaded_here = not Path('/sys/module/nbd').exists()
        claimed = mounted = False
        device = None
        mountpoint = root/'mounted'
        mountpoint.mkdir()
        try:
            if loaded_here:
                run(['sudo','-n','/usr/sbin/modprobe','nbd','nbds_max=1','max_part=8'])
            for candidate in sorted(Path('/sys/block').glob('nbd*'),reverse=True):
                if not (candidate/'pid').exists() and (candidate/'size').read_text().strip() == '0':
                    device = '/dev/'+candidate.name
                    break
            if device is None:
                raise RuntimeError('no unused local NBD device')
            run(['sudo','-n','/usr/sbin/nbd-client','-N','all','-R','-x','-F',str(root/'client/client-cert.pem'),'-K',str(root/'client/client-key.pem'),'-A',str(root/'certs/ca.crt'),'-H','localhost','127.0.0.1',port,device])
            claimed = True
            assert capture(['sudo','-n','/usr/sbin/blockdev','--getro',device]) == '1'
            for attempt in range(50):
                if Path(device+'p1').exists():
                    break
                time.sleep(0.1)
            run(['sudo','-n','mount','-o','ro,nosuid,nodev,noexec,usefree',device+'p1',str(mountpoint)])
            mounted = True
            # Device discovery and mount readahead can outlive mount(8).
            # Wait for the read counters and queue to settle before attributing
            # any traffic to the free-space query itself.
            prior_stats = api('/api/performance/summary')['host']['nbd']['counters']
            stable = 0
            for attempt in range(100):
                time.sleep(0.1)
                current = api('/api/performance/summary')['host']['nbd']['counters']
                if all(current[key] == prior_stats[key] for key in ('bytes_sent','read_requests')) and current.get('queue_depth',0) == 0:
                    stable += 1
                else:
                    stable = 0
                prior_stats = current
                if stable >= 5:
                    break
            else:
                raise RuntimeError('kernel mount traffic did not settle')
            free_clusters = capture(['sudo','-n','stat','-f','-c','%f',str(mountpoint)])
            after_stats = api('/api/performance/summary')['host']['nbd']['counters']
            result['free_space_query'] = {'mount_option':'usefree','free_clusters':int(free_clusters),'nbd_bytes':after_stats['bytes_sent']-prior_stats['bytes_sent'],'nbd_requests':after_stats['read_requests']-prior_stats['read_requests']}
            relative = 'games/Audit GameCube [GAUD01]/game.iso' if platform == 'gamecube' else 'wbfs/TEST03.wbfs'
            digest = capture(['sudo','-n','sha256sum',str(mountpoint/relative)]).split()[0]
            assert digest == before
            denied = sp.run(['sudo','-n','dd','if=/dev/zero','of='+device,'bs=512','count=1','conv=notrunc','status=none'],stdout=log,stderr=log).returncode
            assert denied != 0, 'kernel export unexpectedly writable'
            run(['sudo','-n','umount',str(mountpoint)])
            mounted = False
            run(['sudo','-n','/usr/sbin/fsck.vfat','-n',device+'p1'])
            result['kernel_nbd'] = {'status':'PASS','synthetic_payload_sha256':digest,'mounted_readonly':True,'block_write_rejected':True,'fsck_readonly':'PASS'}
        finally:
            if mounted:
                run(['sudo','-n','umount',str(mountpoint)])
            if claimed:
                run(['sudo','-n','/usr/sbin/nbd-client','-d',device])
            if loaded_here:
                run(['sudo','-n','/usr/sbin/modprobe','-r','nbd'])
            lock.close()
    run(['sudo', '-n', 'docker', 'restart', name])
    for attempt in range(60):
        if sp.run(['sudo', '-n', 'docker', 'exec', name, '/wiibridge-host', 'healthcheck'],stdout=log,stderr=log).returncode == 0:
            break
        time.sleep(0.2)
    else:
        raise RuntimeError('restart did not become healthy')
    assert before == hashlib.sha256(source.read_bytes()).hexdigest()
    info = json.loads(capture(['sudo', '-n', 'docker', 'inspect', name]))[0]
    result.update(status='PASS', image_id=info['Image'], readonly_root=info['HostConfig']['ReadonlyRootfs'], cap_drop=info['HostConfig']['CapDrop'], virtual_bytes=size, fat_bytes=fatsectors*512, verified_reads=request, tests=['hardened OCI startup','healthcheck','libnbd mTLS','read-only export','plaintext rejected','MBR and FAT boot signatures','both FAT copies equal over NBD','restart with catalog persistence','source bytes unchanged'])
finally:
    if started:
        sp.run(['sudo','-n','docker','logs',name],stdout=log,stderr=log)
        sp.run(['sudo','-n','docker','rm','-f',name],stdout=log,stderr=log)
    env.unlink(missing_ok=True)
    (out/'result.json').write_text(json.dumps(result,indent=2)+'\n')
    log.close()
    print(json.dumps(result,indent=2))
