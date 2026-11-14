"""Offline release regression tests: real Git and packaging logic, synthetic
Go, Syft and GitHub.

Run with: python3 scripts/release_test.py
No credentials, network access, Go compiler, or Syft are used.
"""

import hashlib
import io
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

class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="kapten-release-test-")
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.root = self.base / "source"
        self.root.mkdir()
        self.state = self.base / "state"
        self.state.mkdir()
        self.env = dict(os.environ, RELEASE_FIXTURE=str(self.state),
                        GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1",
                        GIT_AUTHOR_NAME="Fixture", GIT_AUTHOR_EMAIL="fixture@example.invalid",
                        GIT_COMMITTER_NAME="Fixture", GIT_COMMITTER_EMAIL="fixture@example.invalid")
        stubs = self.base / "stubs"
        stubs.mkdir()
        self.env["PATH"] = str(stubs) + os.pathsep + os.environ["PATH"]
        scripts = self.root / "scripts"
        scripts.mkdir(parents=True)
        for name in ("release-common", "release-next", "release-notes"):
            shutil.copy2(SCRIPTS / name, scripts / name)
        (self.root / ".gitignore").write_text("dist/\n")
        self.git("init", "-q", "--initial-branch=main")
        self.git("add", ".")
        self.git("commit", "-qm", "feat: CLI fixture")
        self.git("init", "-q", "--bare", str(self.state / "source.git"))
        self.git("remote", "add", "origin", str(self.state / "source.git"))
        self.git("push", "-q", "origin", "main")

    def executable(self, path, body):
        path.write_text(f"#!{sys.executable}\n" + body)
        path.chmod(0o755)

    def git(self, *args, cwd=None):
        return subprocess.check_output(["git", *args], cwd=cwd or self.root,
                                       env=self.env, text=True, stderr=subprocess.PIPE).strip()

    def commit(self, message, push=True):
        self.git("commit", "-q", "--allow-empty", "-m", message)
        if push:
            self.git("push", "-q", "origin", "main")

    def script(self, name, *args, success=True, **environment):
        result = subprocess.run([str(self.root / "scripts" / name), *args], cwd=self.root,
                                env=dict(self.env, **environment), text=True,
                                capture_output=True, timeout=30)
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def remote_tags(self):
        return self.git("--git-dir", str(self.state / "source.git"), "tag", "--list").split()

    def next_version(self, success=True):
        return self.script("release-next", success=success)

    def test_next_version_starts_at_0_1_0(self):
        self.assertEqual(self.next_version().stdout.strip(), "0.1.0")

    def test_next_version_follows_the_commit_titles(self):
        self.git("tag", "v0.1.0")
        for commits, expected in ((["fix: a bug", "docs: a page"], "0.1.1"),
                                  (["feat(cli): a command", "fix: another bug"], "0.2.0"),
                                  (["feat!: a breaking change"], "1.0.0"),
                                  (["refactor: a cleanup\n\nBREAKING CHANGE: a flag is gone"], "2.0.0")):
            with self.subTest(expected=expected):
                for message in commits:
                    self.commit(message, push=False)
                self.assertEqual(self.next_version().stdout.strip(), expected)
                self.git("tag", "v" + expected)
        self.commit("feat!: drop a command", push=False)
        self.assertEqual(self.next_version().stdout.strip(), "3.0.0")

    def test_next_version_ignores_prereleases_and_needs_a_change(self):
        self.git("tag", "v0.1.0-rc.1")
        self.commit("ci: a job", push=False)
        self.assertEqual(self.next_version().stdout.strip(), "0.1.0")
        self.git("tag", "v0.1.0")
        self.commit("docs: a page", push=False)
        self.commit("test: a case", push=False)
        result = self.next_version(success=False)
        self.assertIn("nothing to release", result.stderr)

    def test_notes_list_commits_since_the_latest_release(self):
        self.git("tag", "-a", "v1.0.0", "-m", "v1.0.0")
        self.commit("feat: first change", push=False)
        self.git("tag", "-a", "v1.1.0-rc.1", "-m", "rc")
        self.commit("fix: second change", push=False)
        notes = self.script("release-notes", "v1.1.0").stdout
        self.assertIn("- fix: second change\n- feat: first change\n", notes)
        self.assertNotIn("CLI fixture", notes)
        self.assertIn("compare/v1.0.0...v1.1.0", notes)
        self.git("tag", "-d", "v1.0.0")
        first = self.script("release-notes", "v1.0.0").stdout
        self.assertIn("- feat: CLI fixture", first)
        self.assertNotIn("compare", first)
        self.script("release-notes", "1.x", success=False)

    def test_newer_orders_prereleases_before_their_release(self):
        cases = (("1.2.3", "1.2.3-rc.1", True), ("1.2.3-rc.10", "1.2.3-rc.2", True),
                 ("1.2.3-rc.1", "1.2.3", False), ("1.2.3", "1.2.3", False), ("1.10.0", "1.9.9", True))
        for newer, older, expected in cases:
            with self.subTest(newer=newer, older=older):
                result = subprocess.run(["bash", "-c", 'source "$1"; release_newer "$2" "$3"',
                                         "fixture", str(SCRIPTS / "release-common"), newer, older],
                                        env=self.env)
                self.assertEqual(result.returncode == 0, expected)

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

    def test_npm_packages_hold_each_platform_and_a_launcher(self):
        dist = self.base / "dist"
        dist.mkdir()
        checksums = []
        for target in TARGETS:
            platform, arch = target.split("_")
            suffix = ".exe" if platform == "windows" else ""
            name = f"kapten_1.2.3_{target}"
            # A stand-in kapten that prints its platform and arguments
            files = {"kapten" + suffix: f"#!/bin/sh\necho {target} \"$@\"\n",
                     "restish-mcp" + suffix: "#!/bin/sh\n", "README.md": "Synthetic README\n"}
            if platform == "windows":
                archive = dist / (name + ".zip")
                with zipfile.ZipFile(archive, "w") as output:
                    for member, content in files.items():
                        output.writestr(member, content)
            else:
                archive = dist / (name + ".tar.gz")
                with tarfile.open(archive, "w:gz") as output:
                    for member, content in files.items():
                        data = content.encode()
                        info = tarfile.TarInfo(member)
                        info.size, info.mode = len(data), 0o755
                        output.addfile(info, io.BytesIO(data))
            checksums.append(f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n")
        (dist / "checksums.txt").write_text("".join(checksums))

        packages = self.base / "packages"
        subprocess.run([str(SCRIPTS / "release-npm"), "v1.2.3", str(dist), str(packages)],
                       env=self.env, capture_output=True, text=True, check=True, timeout=30)
        launcher = json.loads((packages / "cli/package.json").read_text())
        self.assertEqual(launcher["name"], "@kapten/cli")
        self.assertEqual(launcher["version"], "1.2.3")
        self.assertEqual(launcher["bin"], {"kapten": "bin/kapten.js"})
        self.assertNotIn("scripts", launcher)  # no install scripts: works with --ignore-scripts
        node_platforms = {f"{'win32' if t.startswith('windows') else t.split('_')[0]}-"
                          f"{'x64' if t.endswith('amd64') else 'arm64'}" for t in TARGETS}
        self.assertEqual(launcher["optionalDependencies"],
                         {f"@kapten/cli-{p}": "1.2.3" for p in node_platforms})
        for node_platform in node_platforms:
            package = packages / f"cli-{node_platform}"
            manifest = json.loads((package / "package.json").read_text())
            os_, cpu = node_platform.split("-")
            self.assertEqual((manifest["name"], manifest["version"], manifest["os"], manifest["cpu"]),
                             (f"@kapten/cli-{node_platform}", "1.2.3", [os_], [cpu]))
            self.assertEqual(manifest["repository"]["url"], launcher["repository"]["url"])
            suffix = ".exe" if os_ == "win32" else ""
            for binary in ("kapten", "restish-mcp"):
                self.assertTrue(os.access(package / "bin" / (binary + suffix), os.X_OK))

        # A corrupted archive is not packed
        archive = dist / "kapten_1.2.3_linux_arm64.tar.gz"
        archive.write_bytes(archive.read_bytes() + b"x")
        result = subprocess.run([str(SCRIPTS / "release-npm"), "v1.2.3", str(dist), str(self.base / "bad")],
                                env=self.env, capture_output=True, text=True, timeout=30)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not match checksums.txt", result.stderr)

        # npm's layout: the launcher runs the package of this platform
        node = shutil.which("node")
        if node is None or sys.platform == "win32":
            self.skipTest("node is not installed")
        current = {"linux": "linux", "darwin": "darwin"}[sys.platform] + "-" + \
            {"x86_64": "x64", "amd64": "x64", "aarch64": "arm64", "arm64": "arm64"}[os.uname().machine]
        modules = self.base / "project/node_modules/@kapten"
        modules.mkdir(parents=True)
        shutil.copytree(packages / "cli", modules / "cli")
        shutil.copytree(packages / f"cli-{current}", modules / f"cli-{current}")
        run = subprocess.run([node, str(modules / "cli/bin/kapten.js"), "projects", "list"],
                             capture_output=True, text=True, timeout=30)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual(run.stdout.split(), [current.replace("x64", "amd64").replace("-", "_"), "projects", "list"])
        shutil.rmtree(modules / f"cli-{current}")
        run = subprocess.run([node, str(modules / "cli/bin/kapten.js")], capture_output=True, text=True, timeout=30)
        self.assertEqual(run.returncode, 1)
        self.assertIn(f"no build for {current}", run.stderr)

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

    def test_missing_or_duplicate_checksum_is_rejected(self):
        name = "kapten_1.2.3_darwin_arm64.tar.gz"
        line = f"{'a' * 64}  {name}\n"
        checksums = self.base / "checksums.txt"
        for content, valid in ((line, True), ("", False), (line + line, False),
                               (f"{'a' * 63}  {name}\n", False)):
            with self.subTest(content=content):
                checksums.write_text(content)
                result = subprocess.run(["bash", "-c", 'source "$1"; release_checksum "$2" "$3"',
                                         "fixture", str(SCRIPTS / "release-common"), str(checksums), name],
                                        env=self.env, capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, valid, result.stderr)


if __name__ == "__main__":
    unittest.main()
