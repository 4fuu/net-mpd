class NetMpd < Formula
  desc "Music Player Daemon adapter for NetEase Cloud Music"
  homepage "https://github.com/4fuu/net-mpd"
  version "2026.729.0"
  license "GPL-3.0-only"

  if OS.mac?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-arm64.tar.gz"
      sha256 "8eb4cc100593d09f4aae5d6a97bf2ded0803e7d1adeb69bdfb0a68719bba0c74" # darwin-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-amd64.tar.gz"
      sha256 "2c9d5e35e459a443eb16b03d23e6d96f70cda446de34d52c0612ad893594fe36" # darwin-amd64
    end
  elsif OS.linux?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-arm64.tar.gz"
      sha256 "43290bcf879d43041c62a5b6ece02c59d85d3577e93c76177c7b0a110b702846" # linux-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-amd64.tar.gz"
      sha256 "b1ed1d5bfa8c02fb4528764bbdc3ff3c85110f5ef402ea8816b894b658fb10e0" # linux-amd64
    end
  end

  def install
    bin.install "net-mpd"
    doc.install "README.md", "LICENSE", "THIRD_PARTY_NOTICES.md", "licenses"
  end

  service do
    run opt_bin/"net-mpd"
    keep_alive crashed: true
  end

  # TODO: Add non-Homebrew service integration after graceful shutdown is implemented.

  test do
    assert_equal version.to_s, shell_output("#{bin}/net-mpd -version").strip
  end
end
