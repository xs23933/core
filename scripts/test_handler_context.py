"""Regression checks for the generated context's delivery contract."""
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


class HandlerContextTest(unittest.TestCase):
    def test_generate_check_and_reject_drift_and_overflow(self):
        root = Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory)
            for name in ("scripts/build-handler-context.py", "skills/core-handler.md",
                         "example/standard/internal/handler/user.go"):
                target = fixture / name
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(root / name, target)
            (fixture / "context").mkdir()
            command = [sys.executable, str(fixture / "scripts/build-handler-context.py")]
            def run(*args):
                return subprocess.run(command + list(args), capture_output=True, text=True)
            self.assertEqual(run().returncode, 0)
            self.assertEqual(run("--check").returncode, 0)
            output = fixture / "context/handler-8k.md"
            source = fixture / "example/standard/internal/handler/user.go"
            self.assertIn(source.read_text(), output.read_text())
            output.write_text("stale")
            result = run("--check")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("stale", result.stderr)
            self.assertEqual(run().returncode, 0)
            source.write_text("x" * 6001)
            result = run()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("exceeds", result.stderr)
            self.assertLess(len(output.read_bytes()), 6000)


if __name__ == "__main__":
    unittest.main()
