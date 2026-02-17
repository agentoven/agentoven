# typed: false
# frozen_string_literal: true

# 🏺 AgentOven — Bake production-ready AI agents
# Formula installs: agentoven CLI (Rust) + agentoven-server (Go) + dashboard (React)

class Agentoven < Formula
  desc "Open-source enterprise agent control plane — A2A & MCP native"
  homepage "https://agentoven.dev"
  url "https://github.com/agentoven/agentoven/archive/refs/tags/v0.2.2.tar.gz"
  sha256 "PLACEHOLDER_SHA256"
  license "MIT"
  head "https://github.com/agentoven/agentoven.git", branch: "main"

  depends_on "rust" => :build
  depends_on "go" => :build
  depends_on "node" => :build

  def install
    # ── 1. Build Rust CLI ──────────────────────────────
    system "cargo", "build", "--release", "-p", "agentoven-cli"
    bin.install "target/release/agentoven"

    # ── 2. Build Go control-plane server ───────────────
    cd "control-plane" do
      system "go", "build",
             "-trimpath",
             "-ldflags", "-s -w",
             "-o", bin/"agentoven-server",
             "./cmd/server"
    end

    # ── 3. Build dashboard ─────────────────────────────
    cd "control-plane/dashboard" do
      system "npm", "install", "--ignore-scripts"
      system "npm", "run", "build"
      pkgshare.install "dist" => "dashboard"
    end
  end

  def caveats
    <<~EOS
      🏺 AgentOven has been installed!

      Quick start:
        agentoven dashboard          # start server + open dashboard
        agentoven --help             # see all commands

      The control-plane server can also be run directly:
        agentoven-server             # starts on port 8080

      Dashboard static files are installed at:
        #{pkgshare}/dashboard
    EOS
  end

  test do
    assert_match "agentoven 0.2.2", shell_output("#{bin}/agentoven --version")
    assert_predicate bin/"agentoven-server", :exist?
    assert_predicate pkgshare/"dashboard/index.html", :exist?
  end
end
