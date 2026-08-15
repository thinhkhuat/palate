class Palate < Formula
  desc "Open taste engine — learns style from observable signals"
  homepage "https://github.com/thinhkhuat/palate"
  version "0.1.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/thinhkhuat/palate/releases/download/v0.1.0/palate_0.1.0_darwin_arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    else
      url "https://github.com/thinhkhuat/palate/releases/download/v0.1.0/palate_0.1.0_darwin_amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/thinhkhuat/palate/releases/download/v0.1.0/palate_0.1.0_linux_arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    else
      url "https://github.com/thinhkhuat/palate/releases/download/v0.1.0/palate_0.1.0_linux_amd64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000"
    end
  end

  def install
    bin.install "palate"
  end

  test do
    system "#{bin}/palate", "doctor"
  end
end
