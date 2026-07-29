class NetMpd < Formula
  desc "Music Player Daemon adapter for NetEase Cloud Music"
  homepage "https://github.com/4fuu/net-mpd"
  version "2026.729.1"
  license "GPL-3.0-only"

  if OS.mac?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-arm64.tar.gz"
      sha256 "a2fc192b98ff3c88b29001d016c76ca04bb85455fe21b9951cc654b76802753c" # darwin-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-darwin-amd64.tar.gz"
      sha256 "cb9dd4e5d35a19a0daa6f92a77a58e393b668b1bfaa2542bfafd6e0ddf0c465f" # darwin-amd64
    end
  elsif OS.linux?
    if Hardware::CPU.arm?
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-arm64.tar.gz"
      sha256 "956800b360b0cd03090fd57fb2c1cc3acf5ed324b19dd0157b4c750f28494bab" # linux-arm64
    else
      url "https://github.com/4fuu/net-mpd/releases/download/v#{version}/net-mpd-#{version}-linux-amd64.tar.gz"
      sha256 "4856745759f8ec3605ff5600711b3bad3262a12e7b9bd82527a9fa82b2a4735f" # linux-amd64
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
