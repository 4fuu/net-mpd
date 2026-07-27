class NetMpd < Formula
  desc "Music Player Daemon adapter for NetEase Cloud Music"
  homepage "https://github.com/4fuu/net-mpd"
  version "2026.727.3"
  license "GPL-3.0-only"

  if OS.mac?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-arm64.tar.gz"
      sha256 "db29728d4fc941cdf1be61125ef770cfd233e607de2cedf5c9c91f17f9c57b12" # darwin-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-amd64.tar.gz"
      sha256 "e3598662111315f7ca397951192dca11c57404e4b0273b500337b0071a526382" # darwin-amd64
    end
  elsif OS.linux?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-arm64.tar.gz"
      sha256 "36c6d6b53b1981cdf3b4da6f17f085fc8f4a69a6e9240fc0e00c495677c6ed73" # linux-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-amd64.tar.gz"
      sha256 "2593c4bda1810a54623d92b0c7c0edda3dccfb4209ac17463fc1fb92cd168126" # linux-amd64
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
