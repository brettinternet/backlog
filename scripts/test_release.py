"""Release safety regressions: wrong CI evidence and corrupt downloads."""
import copy
import hashlib
import http.server
import importlib.machinery
import importlib.util
import io
import os
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader("release", str(ROOT / "scripts/release"))
spec = importlib.util.spec_from_loader(loader.name, loader)
release = importlib.util.module_from_spec(spec)
loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def test_ci_gate(self):
        passing = dict(id=1, head_sha="abc", event="push", head_branch="main",
                       head_repository={"full_name": "owner/repo"},
                       status="completed", conclusion="success")
        release.require_ci([{"workflow_runs": [passing]}], "abc", "owner/repo")
        rejected = [[], [{**passing, "head_sha": "other"}],
                    [{**passing, "event": "pull_request"}],
                    [{**passing, "head_branch": "feature"}],
                    [{**passing, "head_repository": {"full_name": "fork/repo"}}],
                    [{**passing, "status": "in_progress"}],
                    [{**passing, "conclusion": "failure"}],
                    [passing, {**passing, "id": 2, "conclusion": "failure"}],
                    [passing, {**passing, "run_attempt": 2, "conclusion": "failure"}]]
        for runs in rejected:
            with self.subTest(runs=runs), self.assertRaises(ValueError):
                release.require_ci([{"workflow_runs": copy.deepcopy(runs)}], "abc", "owner/repo")
        # A successful matching run on a later API page must still be found.
        release.require_ci([{"workflow_runs": []}, {"workflow_runs": [passing]}], "abc", "owner/repo")

    def test_tag_is_data_not_shell(self):
        # Git permits shell metacharacters in tags; validation must precede execution.
        tag = "v0.0.0'$(printf${IFS}BL44_TAG_INJECTION>&2)'invalid"
        subprocess.run(["git", "check-ref-format", f"refs/tags/{tag}"], check=True)
        result = subprocess.run(["task", "release:build", f"VERSION={tag}"],
                                cwd=ROOT, capture_output=True, text=True, timeout=30)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("version must be", result.stderr)
        self.assertNotIn("\nBL44_TAG_INJECTION", result.stderr)

    def test_installer_integrity(self):
        with tempfile.TemporaryDirectory(prefix="bl-installer-test-") as temporary:
            root = Path(temporary)
            downloads = root / "latest/download"
            downloads.mkdir(parents=True)
            arch = "arm64" if platform.machine() in ("arm64", "aarch64") else "amd64"
            name = f"bl_{platform.system().lower()}_{arch}.tar.gz"
            archive = downloads / name
            with tarfile.open(archive, "w:gz") as tar:
                body = b"#!/bin/sh\nprintf 'fixture-version\\n'\n"
                entry = tarfile.TarInfo("bl")
                entry.size, entry.mode = len(body), 0o755
                tar.addfile(entry, io.BytesIO(body))
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            checksums = downloads / "checksums.txt"
            checksums.write_text(f"{digest}  {name}\n")

            class QuietHandler(http.server.SimpleHTTPRequestHandler):
                def __init__(self, *args, **kwargs):
                    super().__init__(*args, directory=root, **kwargs)

                def log_message(self, *_args):
                    pass

            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), QuietHandler)
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            try:
                destination = root / "bin"
                env = {**os.environ, "HOME": str(root), "BL_VERSION": "latest",
                       "BL_INSTALL_DIR": str(destination),
                       "BL_RELEASE_BASE_URL": f"http://127.0.0.1:{server.server_port}"}

                def install():
                    return subprocess.run(["sh", str(ROOT / "install.sh")], env=env,
                                          capture_output=True, text=True, timeout=15)

                result = install()
                self.assertEqual(result.returncode, 0, result.stderr)
                binary = destination / "bl"
                self.assertEqual(subprocess.check_output([str(binary)], text=True), "fixture-version\n")
                original = binary.read_bytes()
                archive.write_bytes(b"corrupted")
                result = install()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Checksum mismatch", result.stderr)
                self.assertEqual(binary.read_bytes(), original)
                for content in ("", f"{digest}  {name}\n{digest}  {name}\n"):
                    checksums.write_text(content)
                    self.assertNotEqual(install().returncode, 0)
                    self.assertEqual(binary.read_bytes(), original)
            finally:
                server.shutdown()
                server.server_close()
                thread.join()


if __name__ == "__main__":
    unittest.main()
