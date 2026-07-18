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
  end

  service do
    run [opt_bin/"vmc", "daemon"]
    working_dir HOMEBREW_PREFIX
    environment_variables PATH: "#{HOMEBREW_PREFIX}/bin:#{HOMEBREW_PREFIX}/sbin:/usr/bin:/bin:/usr/sbin:/sbin",
                          HOME: Dir.home
    log_path var/"log/vmc.log"
    error_log_path var/"log/vmc.log"
    run_type :interval
    # Hourly detect/process; upload is gated by upload_interval (default weekly).
    interval 3600
  end

  def caveats
    <<~EOS
      To allow vmc to read Voice Memos, grant Full Disk Access to the binary:
        System Settings → Privacy & Security → Full Disk Access → add #{opt_bin}/vmc

      Prefer HF_TOKEN in the environment (or a private config file):
        mkdir -p ~/.config/vmc
        cat > ~/.config/vmc/config.toml <<EOF
      hf_repo = "YOUR_USER/voice-memories"
      hf_private = true
      upload_interval = 604800
      EOF
        # launchd does not inherit your shell; put the token in the config or wrap the service.

      After install, once:
        git xet install

      Then start the service (detect/process hourly; Hub upload ~weekly):
        brew services start vmc

      Force an immediate upload:
        vmc upload --force
    EOS
  end

  test do
    assert_match "Voice Memories Curator", shell_output("#{bin}/vmc --help")
  end
end
