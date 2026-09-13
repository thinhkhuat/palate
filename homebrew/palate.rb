class Palate < Formula
  desc "Open taste engine — learns style from observable signals"
  homepage "https://github.com/thinhkhuat/palate"
  license "MIT"
  head "https://github.com/thinhkhuat/palate.git", branch: "main"

  # SHA256s are filled on publish by GoReleaser (see .goreleaser.yaml:brews).
  # Until v0.3.0 is actually published, install from source:
  #   brew install --HEAD thinhkhuat/palate/palate
  # or: go install github.com/thinhkhuat/palate/cmd/palate@latest
  # The stable block below is a placeholder that MUST be regenerated
  # by the release (goreleaser brew); don't brew install from this blob.
  stable do
    url "https://github.com/thinhkhuat/palate/releases/download/v0.3.0/palate_0.3.0_placeholder.tar.gz"
    sha256 "0000000000000000000000000000000000000000000000000000000000000000"
  end

  def install
    bin.install "palate"
  end

  test do
    system "#{bin}/palate", "doctor"
  end
end
