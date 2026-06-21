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
  end

  service do
    run [opt_bin/"vmc", "daemon"]
    working_dir HOMEBREW_PREFIX
    environment_variables PATH: "#{HOMEBREW_PREFIX}/bin:#{HOMEBREW_PREFIX}/sbin:/usr/bin:/bin:/usr/sbin:/sbin",
                          HOME: Dir.home
    log_path var/"log/vmc.log"
    error_log_path var/"log/vmc.log"
    run_type :interval
    interval 3600
  end

  def caveats
    <<~EOS
      To allow vmc to read Voice Memos, grant Full Disk Access to the binary:
        System Settings → Privacy & Security → Full Disk Access → add #{opt_bin}/vmc

      Configure your HF token in ~/.config/vmc/config.toml:
        hf_token = "hf_YOUR_TOKEN"

      Then start the service:
        brew services start vmc
    EOS
  end

  test do
    assert_match "Voice Memories Curator", shell_output("#{bin}/vmc --help")
  end
end
