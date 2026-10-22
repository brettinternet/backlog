"""Regression: dirty primary source must never leak into the shared CLI build."""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

WRAPPER = Path(__file__).with_name("backlog-cli").resolve()


class PrimaryBuildTest(unittest.TestCase):
    def test_committed_source_and_commit_keyed_cache(self):
        with tempfile.TemporaryDirectory(prefix="bl-wrapper-test-") as directory:
            root = Path(directory)
            env = {**os.environ, "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"}
            for key in list(env):
                if key.startswith(("BL_", "WORKLEASE_")) or key in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"):
                    env.pop(key)

            def run(*args, check=True):
                return subprocess.run(args, cwd=root, env=env, text=True, capture_output=True, check=check)

            run("git", "init", "-b", "main")
            run("git", "config", "user.name", "Test")
            run("git", "config", "user.email", "test@example.invalid")
            (root / "go.mod").write_text("module test.invalid/primary\n\ngo 1.27\n")
            (root / "cmd/bl").mkdir(parents=True)
            source = root / "cmd/bl/main.go"
            source.write_text('package main\nimport "fmt"\nfunc main() { fmt.Println("committed") }\n')
            run("git", "add", ".")
            run("git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "initial")
            source.write_text("not valid Go; must not be built\n")
            self.assertEqual(run(sys.executable, str(WRAPPER)).stdout.strip(), "committed")
            builds = root / ".git/backlog-cli"
            first = {path.name for path in builds.iterdir() if path.is_dir()}
            # A task-state commit must reuse the build rather than add another binary.
            (root / "backlog").mkdir()
            (root / "backlog/BL-1.md").write_text("task\n")
            run("git", "add", "backlog")
            run("git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "task")
            self.assertEqual(run(sys.executable, str(WRAPPER)).stdout.strip(), "committed")
            self.assertEqual({path.name for path in builds.iterdir() if path.is_dir()}, first)
            source.write_text('package main\nimport "fmt"\nfunc main() { fmt.Println("updated") }\n')
            run("git", "add", ".")
            run("git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "update")
            self.assertEqual(run(sys.executable, str(WRAPPER)).stdout.strip(), "updated")
            self.assertEqual(len([path for path in builds.iterdir() if path.is_dir()]), 1)
            run("git", "checkout", "--detach")
            self.assertNotEqual(run(sys.executable, str(WRAPPER), check=False).returncode, 0)


if __name__ == "__main__":
    unittest.main()
