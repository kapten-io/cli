"""Offline release regression tests: real Git/cask logic, synthetic build/GitHub.

Run with: python3 scripts/release_test.py
No credentials, network access, Go compiler, or Syft are used.
"""

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import tarfile
import unittest
import zipfile

SCRIPTS = Path(__file__).resolve().parent
TARGETS = [f"{os}_{arch}" for os in ("darwin", "linux", "windows") for arch in ("amd64", "arm64")]

GH_STUB = r'''
import json, os, pathlib, shutil, subprocess, sys
args = sys.argv[1:]
state = pathlib.Path(os.environ["RELEASE_FIXTURE"])
def option(name):
    return args[args.index(name) + 1]
def run(*args):
    return subprocess.check_output(args, text=True).strip()
with (state / "calls").open("a") as log:
    log.write(json.dumps(args) + "\n")
if args[:2] in (["auth", "status"], ["repo", "view"]):
    pass
elif args[:2] == ["repo", "clone"]:
    subprocess.check_call(["git", "clone", "--quiet", str(state / "tap.git"), args[3]])
elif args[0] == "api":
    print(run("git", "--git-dir", str(state / "tap.git"), "show", "HEAD:Casks/kapten.rb"))
elif args[:2] == ["release", "view"]:
    if not (state / "release.json").exists():
        sys.exit(1)
    data = json.loads((state / "release.json").read_text())
    data["assets"] = [{"name": p.name} for p in sorted((state / "assets").iterdir())]
    if "--jq" in args:
        print("\n".join(a["name"] for a in data["assets"]))
    else:
        print(json.dumps(data))
elif args[:2] == ["release", "create"]:
    data = {"isDraft": True, "isPrerelease": "--prerelease" in args,
            "body": pathlib.Path(option("--notes-file")).read_text().rstrip("\n"),
            "url": "https://example.invalid/release"}
    (state / "release.json").write_text(json.dumps(data))
elif args[:2] == ["release", "upload"]:
    shutil.copyfile(args[3], state / "assets" / pathlib.Path(args[3]).name)
elif args[:2] == ["release", "edit"]:
    data = json.loads((state / "release.json").read_text())
    data["isDraft"] = False
    (state / "release.json").write_text(json.dumps(data))
elif args[:2] == ["release", "download"]:
    destination = pathlib.Path(option("--dir"))
    for asset in (state / "assets").iterdir():
        shutil.copyfile(asset, destination / asset.name)
        if os.environ.get("CORRUPT_DOWNLOAD") == asset.name:
            with (destination / asset.name).open("ab") as output:
                output.write(b"corruption")
else:
    sys.exit("Unexpected gh invocation: " + repr(args))
'''

BUILD_STUB = r'''
import hashlib, os, pathlib, shutil, subprocess, sys
root = pathlib.Path.cwd()
state = pathlib.Path(os.environ["RELEASE_FIXTURE"])
if os.environ.get("FAIL_BUILD") == "1":
    sys.exit("build must not run for a published release")
counter = state / "build-count"
count = int(counter.read_text()) + 1 if counter.exists() else 1
counter.write_text(str(count))
dist = root / "dist"
shutil.rmtree(dist, ignore_errors=True)
dist.mkdir()
version = sys.argv[1][1:]
checksums = []
for platform in ("darwin", "linux", "windows"):
    for arch in ("amd64", "arm64"):
        extension = "zip" if platform == "windows" else "tar.gz"
        name = f"kapten_{version}_{platform}_{arch}.{extension}"
        for artifact in (name, name + ".sbom.json"):
            data = f"{artifact}: build {count}".encode()
            (dist / artifact).write_bytes(data)
            checksums.append(f"{hashlib.sha256(data).hexdigest()}  {artifact}\n")
(dist / "checksums.txt").write_text("".join(checksums))
subprocess.check_call([str(root / "scripts/release-cask"), sys.argv[1], str(dist / "Casks/kapten.rb")])
'''


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="kapten-release-test-")
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.root = self.base / "source"
        self.root.mkdir()
        self.state = self.base / "state"
        self.state.mkdir()
        (self.state / "assets").mkdir()
        self.env = dict(os.environ, RELEASE_FIXTURE=str(self.state),
                        GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1",
                        GIT_AUTHOR_NAME="Fixture", GIT_AUTHOR_EMAIL="fixture@example.invalid",
                        GIT_COMMITTER_NAME="Fixture", GIT_COMMITTER_EMAIL="fixture@example.invalid")
        stubs = self.base / "stubs"
        stubs.mkdir()
        self.env["PATH"] = str(stubs) + os.pathsep + os.environ["PATH"]
        self.executable(stubs / "gh", GH_STUB)
        scripts = self.root / "scripts"
        scripts.mkdir(parents=True)
        for name in ("release-publish", "release-cask", "release-common"):
            shutil.copy2(SCRIPTS / name, scripts / name)
        self.executable(scripts / "release-build", BUILD_STUB)
        (self.root / ".gitignore").write_text("dist/\n")
        self.git("init", "-q", "--initial-branch=main")
        self.git("add", ".")
        self.git("commit", "-qm", "CLI fixture")
        self.git("init", "-q", "--bare", str(self.state / "source.git"))
        self.git("remote", "add", "origin", str(self.state / "source.git"))
        tap = self.base / "tap"
        tap.mkdir()
        self.git("init", "-q", "--initial-branch=main", cwd=tap)
        (tap / "README.md").write_text("Fixture tap\n")
        self.git("add", ".", cwd=tap)
        self.git("commit", "-qm", "Initial tap", cwd=tap)
        self.git("clone", "-q", "--bare", str(tap), str(self.state / "tap.git"))

    def executable(self, path, body):
        path.write_text(f"#!{sys.executable}\n" + body)
        path.chmod(0o755)

    def git(self, *args, cwd=None):
        return subprocess.check_output(["git", *args], cwd=cwd or self.root,
                                       env=self.env, text=True, stderr=subprocess.PIPE).strip()

    def publish(self, success=True, **environment):
        result = subprocess.run([str(self.root / "scripts/release-publish"), "v1.2.3"],
                                cwd=self.root, env=dict(self.env, **environment),
                                text=True, capture_output=True, timeout=30)
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def tap_cask(self):
        return self.git("--git-dir", str(self.state / "tap.git"), "show", "HEAD:Casks/kapten.rb")

    def calls(self):
        return [json.loads(line) for line in (self.state / "calls").read_text().splitlines()]

    def test_first_publication_commits_new_cask(self):
        self.publish()
        self.assertIn('version "1.2.3"', self.tap_cask())
        self.assertIn('  binary "kapten"', self.tap_cask())
        self.assertIn('  binary "restish-mcp"', self.tap_cask())
        self.assertFalse(json.loads((self.state / "release.json").read_text())["isDraft"])
        checksums = (self.state / "assets/checksums.txt").read_text()
        for target in TARGETS[:4]:
            name = f"kapten_1.2.3_{target}.tar.gz"
            digest = hashlib.sha256((self.state / "assets" / name).read_bytes()).hexdigest()
            self.assertIn(digest, checksums)
            self.assertIn(digest, self.tap_cask())

    def test_retry_keeps_published_checksums_and_does_not_commit_again(self):
        self.publish()
        original_cask = self.tap_cask()
        original_tap_head = self.git("--git-dir", str(self.state / "tap.git"), "rev-parse", "HEAD")
        upload_count = sum(call[:2] == ["release", "upload"] for call in self.calls())
        shutil.rmtree(self.root / "dist")
        self.publish(FAIL_BUILD="1")
        self.assertEqual(original_cask, self.tap_cask())
        self.assertEqual(original_tap_head, self.git("--git-dir", str(self.state / "tap.git"), "rev-parse", "HEAD"))
        self.assertEqual(upload_count, sum(call[:2] == ["release", "upload"] for call in self.calls()))
        self.assertEqual((self.state / "build-count").read_text(), "1")
        self.assertFalse((self.root / "dist").exists())

    def test_retry_recovers_from_failed_tap_push(self):
        marker = self.state / "reject-once"
        marker.touch()
        hook = self.state / "tap.git/hooks/pre-receive"
        self.executable(hook, f"import pathlib, sys\np = pathlib.Path({str(marker)!r})\n"
                        "if p.exists():\n    p.unlink()\n    sys.exit(1)\n")
        self.publish(success=False)
        self.assertFalse(json.loads((self.state / "release.json").read_text())["isDraft"])
        self.publish(FAIL_BUILD="1")
        name = "kapten_1.2.3_darwin_arm64.tar.gz"
        self.assertIn(hashlib.sha256((self.state / "assets" / name).read_bytes()).hexdigest(), self.tap_cask())

    def test_draft_retry_rebuilds_and_replaces_assets(self):
        self.publish()
        first = (self.state / "assets/checksums.txt").read_bytes()
        data = json.loads((self.state / "release.json").read_text())
        data["isDraft"] = True
        (self.state / "release.json").write_text(json.dumps(data))
        self.publish()
        self.assertEqual((self.state / "build-count").read_text(), "2")
        self.assertNotEqual(first, (self.state / "assets/checksums.txt").read_bytes())
        self.assertFalse(json.loads((self.state / "release.json").read_text())["isDraft"])
        name = "kapten_1.2.3_darwin_arm64.tar.gz"
        self.assertIn(hashlib.sha256((self.state / "assets" / name).read_bytes()).hexdigest(), self.tap_cask())

    def test_missing_published_asset_does_not_update_tap(self):
        self.publish()
        original = self.tap_cask()
        (self.state / "assets/kapten_1.2.3_windows_arm64.zip").unlink()
        result = self.publish(success=False, FAIL_BUILD="1")
        self.assertIn("published release is missing", result.stderr)
        self.assertEqual(original, self.tap_cask())

    def test_real_packaging_uses_shared_asset_manifest(self):
        stubs = self.base / "stubs"
        self.executable(stubs / "go", 'import json, os, pathlib, sys\nargs = sys.argv[1:]\n'
                        'state = pathlib.Path(os.environ["RELEASE_FIXTURE"])\n'
                        'with (state / "go-calls").open("a") as log:\n'
                        '    log.write(json.dumps({"args": args, "cwd": str(pathlib.Path.cwd()), '
                        '"os": os.environ["GOOS"], "arch": os.environ["GOARCH"]}) + "\\n")\n'
                        'output = pathlib.Path(args[args.index("-o") + 1])\n'
                        'output.write_bytes(args[-1].encode())\noutput.chmod(0o755)\n')
        self.executable(stubs / "syft", 'import pathlib, sys\n'
                        'output = next(arg.split("=", 1)[1] for arg in sys.argv[1:] if arg.startswith("spdx-json="))\n'
                        'pathlib.Path(output).write_text("{}\\n")\n')
        (stubs / "python3").symlink_to(sys.executable)
        (self.root / "README.md").write_text("Synthetic CLI README\n")
        subprocess.run([str(SCRIPTS / "release-build"), "v1.2.3"], cwd=self.root,
                       env=self.env, text=True, capture_output=True, check=True, timeout=30)
        output = subprocess.check_output(["bash", "-c", 'source "$1"; release_assets "$2"',
                                          "fixture", str(SCRIPTS / "release-common"), "1.2.3"],
                                         env=self.env, text=True)
        names = output.splitlines()
        dist = self.root / "dist"
        self.assertEqual(sorted(names), sorted(path.name for path in dist.iterdir() if path.is_file()))
        checksums = dict(line.split()[::-1] for line in (dist / "checksums.txt").read_text().splitlines())
        for name in names:
            if name != "checksums.txt":
                self.assertEqual(checksums[name], hashlib.sha256((dist / name).read_bytes()).hexdigest())
        cask = (dist / "Casks/kapten.rb").read_text()
        self.assertIn('  binary "kapten"', cask)
        self.assertIn('  binary "restish-mcp"', cask)
        calls = [json.loads(line) for line in (self.state / "go-calls").read_text().splitlines()]
        self.assertEqual(len(calls), 12)
        for target in TARGETS:
            platform, arch = target.split("_")
            suffix = ".exe" if platform == "windows" else ""
            expected = {"kapten" + suffix: b"./cmd/kapten",
                        "restish-mcp" + suffix: b"github.com/rest-sh/restish/v2/cmd/restish-mcp",
                        "README.md": b"Synthetic CLI README\n"}
            name = f"kapten_1.2.3_{target}"
            if platform == "windows":
                with zipfile.ZipFile(dist / (name + ".zip")) as archive:
                    self.assertEqual(set(archive.namelist()), set(expected))
                    for member, content in expected.items():
                        self.assertEqual(archive.read(member), content)
            else:
                with tarfile.open(dist / (name + ".tar.gz")) as archive:
                    self.assertEqual(set(archive.getnames()), set(expected))
                    for member, content in expected.items():
                        self.assertEqual(archive.extractfile(member).read(), content)
                        if member != "README.md":
                            self.assertTrue(archive.getmember(member).mode & 0o111)
            target_calls = [call for call in calls if call["os"] == platform and call["arch"] == arch]
            self.assertEqual(len(target_calls), 2)
            self.assertEqual({call["args"][-1] for call in target_calls},
                             {"./cmd/kapten", "github.com/rest-sh/restish/v2/cmd/restish-mcp"})
            for call in target_calls:
                self.assertEqual(Path(call["cwd"]).resolve(), self.root.resolve())
                self.assertEqual(call["args"][0], "build")
                self.assertIn("-mod=readonly", call["args"])
                self.assertNotIn("@", call["args"][-1])
                binary = "kapten" if call["args"][-1] == "./cmd/kapten" else "restish-mcp"
                self.assertEqual(Path(call["args"][call["args"].index("-o") + 1]).resolve(),
                                 (dist / "bin" / target / (binary + suffix)).resolve())

    def test_development_build_uses_adjacent_pinned_helper(self):
        config = (SCRIPTS.parent / "mise.toml").read_text()
        build = config.split("[tasks.build]\n", 1)[1].split("\n[tasks.", 1)[0]
        # A directory output lets Go add the platform's executable suffix.
        self.assertIn('"go build -mod=readonly -o . ./cmd/kapten"', build)
        self.assertIn('"go build -mod=readonly -o . '
                      'github.com/rest-sh/restish/v2/cmd/restish-mcp"', build)
        self.assertNotIn("go install", build)
        self.assertNotIn("@", build)

    def test_version_validation_is_shared(self):
        for version in ("v1.2.3", "v0.0.0-snapshot", "v1.2.3-rc.1", "v1.2.3-alpha-beta.0"):
            result = subprocess.run(["bash", "-c", 'source "$1"; release_version "$2"',
                                     "fixture", str(SCRIPTS / "release-common"), version],
                                    env=self.env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), version[1:])
        for version in ("v01.2.3", "v1.2.3-", "v1.2.3-rc..1", "v1.2.3-01", "v1.2.3-rc.01"):
            result = subprocess.run(["bash", "-c", 'source "$1"; release_version "$2"',
                                     "fixture", str(SCRIPTS / "release-common"), version],
                                    env=self.env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0, version)

    def test_corrupt_published_download_does_not_update_tap(self):
        self.publish()
        original = self.tap_cask()
        result = self.publish(success=False, CORRUPT_DOWNLOAD="kapten_1.2.3_darwin_arm64.tar.gz")
        self.assertIn("published checksum is missing, invalid, or mismatched", result.stderr)
        self.assertEqual(original, self.tap_cask())

    def test_missing_or_duplicate_checksum_is_rejected(self):
        self.publish()
        original = self.tap_cask()
        checksums = self.state / "assets/checksums.txt"
        content = checksums.read_text()
        name = "kapten_1.2.3_darwin_arm64.tar.gz"
        line = next(line for line in content.splitlines(keepends=True) if line.rstrip().endswith(name))
        for invalid in (content.replace(line, ""), content + line):
            with self.subTest(checksums=invalid):
                checksums.write_text(invalid)
                self.publish(success=False)
                self.assertEqual(original, self.tap_cask())
                output = self.base / "invalid/Casks/kapten.rb"
                output.parent.parent.mkdir(exist_ok=True)
                (output.parent.parent / "checksums.txt").write_text(invalid)
                result = subprocess.run([str(SCRIPTS / "release-cask"), "v1.2.3", str(output)],
                                        env=self.env, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
