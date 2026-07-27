class NetMpd < Formula
  desc "Music Player Daemon adapter for NetEase Cloud Music"
  homepage "https://github.com/4fuu/net-mpd"
  version "2026.727.2"
  license "GPL-3.0-only"

  if OS.mac?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-arm64.tar.gz"
      sha256 "c763787cb2cfbb3c4d292a3efd7f3935fce8a071ddab759d2707f1c96515063e" # darwin-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-amd64.tar.gz"
      sha256 "989930f264f7ba39812ebe0093e720a26a3d1d04fd318365d9512c739a53078d" # darwin-amd64
    end
  elsif OS.linux?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-arm64.tar.gz"
      sha256 "9244e1a3661d6d781afb9c0e80f990e040a1287748254209b6cd12f8b0253c12" # linux-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-amd64.tar.gz"
      sha256 "52e3739eab5f5bdd58097125980fcd6c1238e034eada1daac883f9730527a5f4" # linux-amd64
    end
  end

  def install
    bin.install "net-mpd"
    doc.install "README.md", "LICENSE", "THIRD_PARTY_NOTICES.md", "licenses"
  end

  test do
    assert_equal version.to_s, shell_output("#{bin}/net-mpd -version").strip
  end
end
