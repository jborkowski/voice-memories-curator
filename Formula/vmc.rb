class Vmc < Formula
  desc "Voice Memories Curator — extracts macOS Voice Memos to Hugging Face"
  homepage "https://github.com/jborkowski/voice-memories-curator"
  head "https://github.com/jborkowski/voice-memories-curator.git", branch: "main"
  license "MIT"

  depends_on :macos
  depends_on "go" => :build
  depends_on "ffmpeg"
  depends_on "git-xet"

  def install
    ENV["CGO_ENABLED"] = "1"
    system "go", "build", *std_go_args(ldflags: "-s -w"), "."
    (share/"vmc").install "scripts/fix_hf_parquet.py"
    bin.install "scripts/grant-fda.sh" => "vmc-grant-fda"
  end

  service do
    run [opt_bin/"vmc", "daemon"]
    working_dir HOMEBREW_PREFIX
    environment_variables PATH: "#{HOMEBREW_PREFIX}/bin:#{HOMEBREW_PREFIX}/sbin:/usr/bin:/bin:/usr/sbin:/sbin",
                          HOME: Dir.home
    log_path var/"log/vmc.log"
    error_log_path var/"log/vmc.log"
    run_type :interval
    # Hourly detect → process → upload (upload pushes only shards missing on Hub).
    interval 3600
  end

  def caveats
    <<~EOS
      Full Disk Access (required for brew services):
        vmc-grant-fda
      Then drag ~/Desktop/vmc into the Full Disk Access list and turn it ON.
      Docs: https://github.com/jborkowski/voice-memories-curator/blob/main/docs/02-ops-flow.md

      Config ~/.config/vmc/config.toml (hf_token + hf_repo). Prefer HF_TOKEN env if you wrap the service.

      After install, once:
        git xet install
        vmc-grant-fda
        brew services start vmc
    EOS
  end

  test do
    assert_match "Voice Memories Curator", shell_output("#{bin}/vmc --help")
  end
end
