#!/usr/bin/env python3
"""Exercise real read-only mounts and recovery APIs with synthetic games only.

Run `make server`, then run this script with access to Docker and root-owned
container test data (for example, `sudo python3 tests/truenas/separate-libraries-test.py`).
"""

import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import sqlite3
import ssl
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


REPO = Path(__file__).resolve().parents[2]


def run(*args):
    return subprocess.check_output(args, stderr=subprocess.STDOUT, text=True).strip()


def synthetic_games(wii, gamecube):
    with (wii / "synthetic.wbfs").open("wb") as output:
        header = bytearray(512)
        header[:4] = b"WBFS"
        struct.pack_into(">I", header, 4, (2 << 20) // 512)
        header[8], header[9], header[12] = 9, 20, 1
        output.write(header)
        output.seek(1 << 20)
        output.write(b"SWII01" + bytes(26) + b"Synthetic Wii")
        output.truncate(2 << 20)
    with (gamecube / "synthetic.iso").open("wb") as output:
        header = bytearray(0x440)
        header[:6] = b"SGCE01"
        struct.pack_into(">I", header, 0x1C, 0xC2339F3D)
        header[0x20:0x2C] = b"Synthetic GC"
        struct.pack_into(">IIII", header, 0x420, 0x2000, 0x1000, 12, 12)
        output.write(header)
        output.seek(0x1000)
        output.write(struct.pack(">III", 0x01000000, 0, 1))
        output.truncate(2 << 20)


def main():
    root = Path(tempfile.mkdtemp(prefix="wiibridge-libraries-"))
    root.chmod(0o755)
    name = "wiibridge-libraries-" + root.name.rsplit("-", 1)[1]
    image = name + ":test"
    results = []
    token = secrets.token_urlsafe(32)
    base = ""

    def api(path, form=None):
        headers = {"Authorization": "Bearer " + token, "Accept": "application/json"}
        data = None
        if form is not None:
            headers["X-WiiBridge-CSRF"] = "1"
            data = urllib.parse.urlencode(form).encode()
        request = urllib.request.Request(base + path, data=data, headers=headers)
        try:
            response = urllib.request.urlopen(request, context=tls, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            body = response.read().decode()
            try:
                body = json.loads(body)
            except json.JSONDecodeError:
                pass
            return response.status, body

    def expect(path, form=None, status=200):
        actual, body = api(path, form)
        assert actual == status, (path, actual, body)
        return body

    def wait_until(probe, description):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            try:
                if probe():
                    return
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(0.1)
        raise AssertionError("Timed out: " + description)

    def start(wii_source, gc_source, wii_child=""):
        nonlocal base
        run("docker", "run", "-d", "--name", name,
            "--user", "568:568", "--read-only", "--cap-drop", "ALL",
            "--security-opt", "no-new-privileges:true", "--pids-limit", "256",
            "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=32m",
            "--env-file", str(root / "host.env"),
            "--env", "WIIBRIDGE_WII_LIBRARY=/library/wii" + wii_child,
            "--mount", f"type=bind,src={wii_source},dst=/library/wii,readonly",
            "--mount", f"type=bind,src={gc_source},dst=/library/gamecube,readonly",
            "--mount", f"type=bind,src={root / 'data'},dst=/data",
            "--mount", f"type=bind,src={root / 'certs'},dst=/certs,readonly",
            "-p", "127.0.0.1::8445", image)
        port = run("docker", "port", name, "8445/tcp").rsplit(":", 1)[1]
        base = "https://127.0.0.1:" + port
        wait_until(lambda: isinstance(api("/api/v1/sources")[1], dict)
                   and "sources" in api("/api/v1/sources")[1], "source API")
        wait_until(lambda: api("/api/v1/sources")[1]["sources"]["gamecube"]["state"]
                   != "unknown", "GameCube source initialization")

    def stop():
        run("docker", "rm", "-f", name)

    def build_gc():
        expect("/api/v1/gamecube/library/build", {}, 202)
        def complete():
            status = api("/api/v1/gamecube/library")[1]
            return status.get("ready") and status.get("progress", {}).get("state") == "Ready"
        wait_until(complete, "GameCube build")

    try:
        shutil.copyfile(REPO / "build/bin/wiibridge-host", root / "wiibridge-host")
        (root / "wiibridge-host").chmod(0o555)
        (root / "Dockerfile").write_text("FROM scratch\nCOPY wiibridge-host /wiibridge-host\nENTRYPOINT [\"/wiibridge-host\"]\n")
        run("docker", "build", "--network=none", "-t", image, str(root))
        for directory in ["wii", "gc", "empty", "data", "certs"]:
            (root / directory).mkdir(mode=0o777 if directory == "data" else 0o755)
        (root / "data").chmod(0o777)
        synthetic_games(root / "wii", root / "gc")
        before = {p.suffix: hashlib.sha256(p.read_bytes()).hexdigest()
                  for p in [root / "wii/synthetic.wbfs", root / "gc/synthetic.iso"]}
        run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
            "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1",
            "-keyout", str(root / "certs/server.key"), "-out", str(root / "certs/server.crt"))
        shutil.copyfile(root / "certs/server.crt", root / "certs/clients-ca.crt")
        (root / "certs/server.key").chmod(0o644)
        tls = ssl.create_default_context(cafile=str(root / "certs/server.crt"))
        (root / "host.env").write_text("WIIBRIDGE_ADMIN_TOKEN=" + token + "\n"
                                       "WIIBRIDGE_WII_LIBRARY=/library/wii\n"
                                       "WIIBRIDGE_GAMECUBE_LIBRARY=/library/gamecube\n")
        (root / "host.env").chmod(0o600)
        start(root / "wii", root / "gc")
        sources = expect("/api/v1/sources")["sources"]
        assert all(sources[p]["state"] == "available" for p in ["wii", "gamecube"]), sources
        expect("/api/v1/scan", {"platform": "all"})
        build_gc()
        results.append("separate read-only mounts scan and build")

        (root / "gc/synthetic.iso").rename(root / "gc/moved.iso")
        expect("/api/v1/scan", {"platform": "gamecube"})
        assert expect("/api/v1/sources")["sources"]["gamecube"]["state"] == "available"
        build_gc()
        results.append("moved GameCube file rebuild")
        stop()


        # Persisted device numbers may change without changing a filesystem or
        # directory. Keep real readonly bind mounts and restart the service.
        database_path = next((root / "data").glob("*.sqlite3"))
        with sqlite3.connect(database_path) as database:
            stable = database.execute("SELECT COUNT(*) FROM source_roots WHERE filesystem_id != ''").fetchone()[0]
            if stable:
                database.execute("UPDATE source_roots SET last_known_device=last_known_device+1,last_known_mount_info='synthetic:old:device' WHERE filesystem_id != ''")
        if stable:
            start(root / "wii", root / "gc")
            assert all(value["state"] == "available" for value in expect("/api/v1/sources")["sources"].values())
            assert expect("/api/v1/gamecube/library")["ready"]
            expect("/readyz")
            results.append("persistent filesystem identity survives saved device renumbering and restart")
            stop()
        else:
            results.append("persistent identity container check skipped: use ext4 or ZFS TMPDIR")

        database_path = next((root / "data").glob("*.sqlite3"))
        with sqlite3.connect(database_path) as database:
            database.execute("UPDATE source_roots SET filesystem_id='',root_inode=0,last_known_mount_info=? WHERE root_path=?",
                             ("synthetic:prior:mount", "/library/gamecube"))
        start(root / "wii", root / "gc")
        assert expect("/api/v1/sources")["sources"]["gamecube"]["state"] == "mount-missing"
        expect("/readyz")
        expect("/api/v1/scan", {"platform": "gamecube"}, 503)
        expect("/api/v1/sources/relocate", {"platform": "gamecube"}, 400)
        expect("/api/v1/sources/relocate", {"platform": "gamecube", "confirm": "relocate"})
        results.append("persisted stale mount rejected then explicitly recovered")
        stop()

        (root / "empty/notes.txt").write_text("synthetic empty replacement")
        start(root / "wii", root / "empty")
        expect("/api/v1/sources/relocate", {"platform": "gamecube", "confirm": "relocate"}, 503)
        assert len(expect("/api/v1/gamecube")["games"]) == 1
        expect("/readyz")
        results.append("empty replacement preserves GameCube catalog and Wii readiness")
        stop()

        start(root / "empty", root / "gc")
        assert expect("/api/v1/sources")["sources"]["wii"]["state"] in ("mount-missing", "changed")
        build_gc()
        expect("/api/v1/export/gamecube", {})
        expect("/readyz")
        results.append("GameCube builds and activates while Wii mount is unavailable")
        stop()
        (root / "delayed/catalog").mkdir(parents=True)
        shutil.copyfile(root / "wii/synthetic.wbfs", root / "delayed/catalog/synthetic.wbfs")
        start(root / "delayed", root / "gc", "/catalog")
        expect("/readyz")
        stop()
        (root / "delayed/catalog").rename(root / "delayed/offline")
        start(root / "delayed", root / "gc", "/catalog")
        assert expect("/api/v1/sources")["sources"]["wii"]["state"] == "mount-missing"
        assert len(expect("/api/v1/scan")["games"]) == 1
        (root / "delayed/offline").rename(root / "delayed/catalog")
        wait_until(lambda: api("/api/v1/sources")[1]["sources"]["wii"]["state"] == "available", "automatic return of original library")
        expect("/api/v1/export/wii", {})
        expect("/readyz")
        results.append("late source automatically rescanned after startup without catalog loss")
        assert hashlib.sha256((root / "wii/synthetic.wbfs").read_bytes()).hexdigest() == before[".wbfs"]
        assert hashlib.sha256((root / "gc/moved.iso").read_bytes()).hexdigest() == before[".iso"]
        results.append("source bytes unchanged")
        print(json.dumps({"status": "PASS", "checks": results}, indent=2))
    finally:
        subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(["docker", "image", "rm", image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        shutil.rmtree(root)


if __name__ == "__main__":
    main()
