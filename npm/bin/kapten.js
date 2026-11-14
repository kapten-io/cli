#!/usr/bin/env node
// Runs the kapten executable of this platform, installed by npm as one of
// @kapten/cli's optional dependencies (@kapten/cli-<os>-<cpu>).
"use strict";

const { spawnSync } = require("node:child_process");
const path = require("node:path");

const platform = `${process.platform}-${process.arch}`;
const executable = process.platform === "win32" ? "kapten.exe" : "kapten";

let binary;
try {
  const manifest = require.resolve(`@kapten/cli-${platform}/package.json`);
  binary = path.join(path.dirname(manifest), "bin", executable);
} catch {
  console.error(
    `kapten: no build for ${platform} was installed. Reinstall @kapten/cli ` +
      "without --no-optional (or --omit=optional), or install the CLI another " +
      "way: https://github.com/kapten-io/cli#install",
  );
  process.exit(1);
}

const result = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  console.error(`kapten: ${result.error.message}`);
  process.exit(1);
}
if (result.signal) {
  process.kill(process.pid, result.signal);
} else {
  process.exit(result.status ?? 1);
}
