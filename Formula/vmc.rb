class Vmc < Formula
  desc "Voice Memories Curator — extracts macOS Voice Memos to Hugging Face"
  homepage "https://github.com/jborkowski/vmc"
  # url / sha256 — placeholder for actual release tarball
  license "MIT"

  depends_on :macos
  depends_on "go" => :build
  depends_on "ffmpeg"
  depends_on "git-xet"

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "."
  end

  service do
    run [opt_bin/"vmc", "daemon"]
    working_dir HOMEBREW_PREFIX
    log_path var/"log/vmc.log"
    error_log_path var/"log/vmc.log"
    run_type :interval
    interval 3600
  end

  test do
    assert_match "Voice Memories Curator", shell_output("#{bin}/vmc --help")
  end
end
