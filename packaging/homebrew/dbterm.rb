class Dbterm < Formula
  desc "Keyboard-first terminal database client for PostgreSQL, MySQL, SQLite, Turso, and Cloudflare D1"
  homepage "https://shreyam1008.github.io/dbterm/"
  url "https://github.com/shreyam1008/dbterm/archive/refs/tags/v0.4.1.tar.gz"
  # TODO: Replace sha256 with the actual SHA256 of the v0.4.1 source tarball.
  # Run: curl -sL https://github.com/shreyam1008/dbterm/archive/refs/tags/v0.4.1.tar.gz | sha256sum
  sha256 "0000000000000000000000000000000000000000000000000000000000000000"
  license "MIT"
  head "https://github.com/shreyam1008/dbterm.git", branch: "main"

  bottle :unneeded

  depends_on "go" => :build

  def install
    system "go", "build",
      "-trimpath",
      "-buildvcs=false",
      "-ldflags", "-s -w -buildid= -X main.version=#{version}",
      "-o", bin/"dbterm",
      "."
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/dbterm --version 2>&1", 0)
  end
end

# HOW TO PUBLISH THIS FORMULA:
#
# Option A — Personal Homebrew tap (fastest, no review required):
#   1. Create a GitHub repo named: homebrew-tap
#      (full name: github.com/shreyam1008/homebrew-tap)
#   2. Copy this file to: Formula/dbterm.rb inside that repo.
#   3. Users install with:
#        brew tap shreyam1008/tap
#        brew install shreyam1008/tap/dbterm
#
# Option B — Official Homebrew core (requires significant traction):
#   - Requires ~30 forks or evidence of real usage.
#   - Submit PR to https://github.com/Homebrew/homebrew-core
#   - Start with the tap first.
#
# Remember to replace the sha256 placeholder above before publishing.
