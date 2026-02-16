# 🏺 AgentOven Homebrew Tap

Official [Homebrew](https://brew.sh) tap for [AgentOven](https://agentoven.dev) — the open-source enterprise agent control plane.

## Installation

```bash
brew tap agentoven/tap
brew install agentoven
```

Or in one command:

```bash
brew install agentoven/tap/agentoven
```

## What gets installed

| Binary | Description |
|--------|-------------|
| `agentoven` | CLI — register agents, run recipes, launch dashboard |
| `agentoven-server` | Go control-plane API server |
| Dashboard | React UI served by the control plane |

## Usage

```bash
# Launch the dashboard (starts server + opens browser)
agentoven dashboard

# See all commands
agentoven --help

# Run the server directly
agentoven-server
```

## Upgrade

```bash
brew update
brew upgrade agentoven
```

## Uninstall

```bash
brew uninstall agentoven
brew untap agentoven/tap
```

## Publishing (maintainers)

After creating a GitHub release with tag `vX.Y.Z`:

1. Download the source tarball: `https://github.com/agentoven/agentoven/archive/refs/tags/vX.Y.Z.tar.gz`
2. Compute SHA256: `shasum -a 256 vX.Y.Z.tar.gz`
3. Update `Formula/agentoven.rb` with the new URL, version, and SHA256
4. Push to `agentoven/homebrew-tap`

The [release workflow](https://github.com/agentoven/agentoven/blob/main/.github/workflows/release.yml) automates this.

## License

MIT — see [LICENSE](https://github.com/agentoven/agentoven/blob/main/LICENSE)
