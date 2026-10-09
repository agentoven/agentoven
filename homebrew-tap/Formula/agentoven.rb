class Agentoven < Formula
  desc "Open-source enterprise agent control plane, A2A and MCP native"
  homepage "https://agentoven.dev"
  url "https://github.com/agentoven/agentoven/archive/refs/tags/v0.9.0.tar.gz"
  sha256 "f1ff6ce1918b6a34862b9b598bc9bd746f3245f9d95057f6ac7751709414e75d"
  license "Apache-2.0"
  head "https://github.com/agentoven/agentoven.git", branch: "main"

  depends_on "go" => :build
  depends_on "node" => :build
  depends_on "rust" => :build

  def install
    # The CLI.
    system "cargo", "install", *std_cargo_args(path: "crates/agentoven-cli")

    cd "control-plane" do
      # The dashboard is static files the server serves from AGENTOVEN_DASHBOARD_DIR.
      cd "dashboard" do
        system "npm", "ci", "--ignore-scripts"
        system "npm", "run", "build"
        pkgshare.install "dist" => "dashboard"
      end

      # The control plane.
      system "go", "build", *std_go_args(output: libexec/"bin/agentoven-server", ldflags: "-s -w"), "./cmd/server"
    end

    (bin/"agentoven-server").write_env_script libexec/"bin/agentoven-server",
                                              AGENTOVEN_DASHBOARD_DIR: pkgshare/"dashboard"
  end

  def caveats
    <<~EOS
      Start the control plane (port 8080 by default):
        agentoven-server

      Then use the CLI against it:
        agentoven --help

      Data is kept in ~/.agentoven; set AGENTOVEN_DATA_DIR to change it.
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/agentoven --version")
    assert_path_exists pkgshare/"dashboard/index.html"

    port = free_port
    ENV["AGENTOVEN_PORT"] = port.to_s
    ENV["AGENTOVEN_DATA_DIR"] = testpath/"data"
    pid = spawn bin/"agentoven-server"
    begin
      sleep 5
      assert_match "healthy", shell_output("curl -s http://127.0.0.1:#{port}/health")
    ensure
      Process.kill("TERM", pid)
      Process.wait(pid)
    end
  end
end
