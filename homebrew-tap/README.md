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

The formula builds from the release source tarball. After tagging `vX.Y.Z` in `agentoven/agentoven`:

1. Compute the tarball checksum: `curl -sL https://github.com/agentoven/agentoven/archive/refs/tags/vX.Y.Z.tar.gz | shasum -a 256`
2. Update `url` and `sha256` in `Formula/agentoven.rb`.
3. Check it: `brew style`, `brew install --build-from-source`, `brew test` and `brew audit --strict --new --online agentoven/tap/agentoven`.
4. Push to `main`.

The release workflow in `agentoven/agentoven` does steps 1, 2 and 4 when the `HOMEBREW_TAP_TOKEN` secret is set.
