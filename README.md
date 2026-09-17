# Kapten CLI

Release artifacts for the Kapten command-line interface.

The CLI source lives in the [`kapten-io/kapten`](https://github.com/kapten-io/kapten) monorepo. Source tags named `cli/vX.Y.Z` publish unprefixed `vX.Y.Z` releases here.

## Install with Homebrew

```bash
brew install --cask kapten-io/tap/kapten
```

## Install with mise

This repository contains an Aqua registry definition for the release assets. Add it as a custom Aqua registry, then install the CLI:

```toml
[settings]
aqua.registries = ["https://github.com/kapten-io/cli"]

[tools]
"aqua:kapten-io/cli" = "latest"
```

Once the package is available in Aqua's standard registry, the equivalent global command is:

```bash
mise use -g aqua:kapten-io/cli
```

## Direct download

Download the archive for your platform from [Releases](https://github.com/kapten-io/cli/releases) and verify it against `checksums.txt`.
