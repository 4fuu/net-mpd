class NetMpd < Formula
  desc "Music Player Daemon adapter for NetEase Cloud Music"
  homepage "https://github.com/4fuu/net-mpd"
  version "2026.728.0"
  license "GPL-3.0-only"

  if OS.mac?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-arm64.tar.gz"
      sha256 "c9d7ae74b086c61c36e075e0651ce953fdb22182ce4677c2fd4c3a5638ae52c6" # darwin-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-amd64.tar.gz"
      sha256 "d537c77c13cef97e4d3d28005e99cac1eb829134a816c7eb121a46158628ab09" # darwin-amd64
    end
  elsif OS.linux?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-arm64.tar.gz"
      sha256 "50a5288258466cea4f53e198b5f41dcfe8acd32cc139b15d32a371a718fd8e0e" # linux-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-amd64.tar.gz"
      sha256 "f634a82cfc8c3bc2df4896d000c5b8a9ebc44363d15be5f5a0f170741d3a99ac" # linux-amd64
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
