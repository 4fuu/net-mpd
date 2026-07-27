class NetMpd < Formula
  desc "Music Player Daemon adapter for NetEase Cloud Music"
  homepage "https://github.com/4fuu/net-mpd"
  version "2026.727.4"
  license "GPL-3.0-only"

  if OS.mac?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-arm64.tar.gz"
      sha256 "0a82aa0c826b0e66890006422858bf7706310e54e7133b44b04f492c386d8534" # darwin-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-amd64.tar.gz"
      sha256 "3f0a62ac5b39846e4888eec0c88da541b8df31a150aa13ac60bf12e34904030b" # darwin-amd64
    end
  elsif OS.linux?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-arm64.tar.gz"
      sha256 "18d9e0fc4050500d72b416027f4c072a440466a73d56e19f87255934d79f6105" # linux-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-amd64.tar.gz"
      sha256 "a2e9c045825e63889175faa9e573f578f7cee4816a0a98ef8403a3c7710e1fa6" # linux-amd64
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
