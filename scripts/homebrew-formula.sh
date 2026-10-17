#!/bin/sh
# Writes shoal's Homebrew formula for a release to standard output, for
# the release workflow to commit to BT10011/homebrew-tap as
# Formula/shoal.rb:
#
#   scripts/homebrew-formula.sh v2.0.0 dist/SHA256SUMS > shoal.rb
#
# A formula rather than a cask (see the project plan): Homebrew quarantines
# a cask's download and Gatekeeper then blocks an unsigned binary, while a
# formula's download is never quarantined. It installs the release's own
# archives for macOS and Linux, each pinned to the checksum in SHA256SUMS.
set -eu

if [ $# -ne 2 ]; then
	echo "usage: $0 <tag> <SHA256SUMS>" >&2
	exit 2
fi
tag=$1
sums=$2
case "$tag" in
v[0-9]*) ;;
*)
	echo "homebrew-formula: $tag is not a version tag" >&2
	exit 1
	;;
esac
version=${tag#v}
base="https://github.com/BT10011/shoal/releases/download/$tag"

sum() {
	s=$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1 }' "$sums")
	case "$s" in
	[0-9a-f]*) printf '%s' "$s" ;;
	*)
		echo "homebrew-formula: $1 is not listed in $sums" >&2
		exit 1
		;;
	esac
}
darwin_arm64=$(sum shoal-darwin-arm64.tar.gz)
darwin_amd64=$(sum shoal-darwin-amd64.tar.gz)
linux_arm64=$(sum shoal-linux-arm64.tar.gz)
linux_amd64=$(sum shoal-linux-amd64.tar.gz)

cat <<EOF
# Written for each release by shoal's release workflow
# (scripts/homebrew-formula.sh in BT10011/shoal); edits here are replaced.
class Shoal < Formula
  desc "LAN discovery in the terminal that shows how every device was found"
  homepage "https://github.com/BT10011/shoal"
  version "$version"
  license "MIT"

  on_macos do
    on_arm do
      url "$base/shoal-darwin-arm64.tar.gz"
      sha256 "$darwin_arm64"
    end
    on_intel do
      url "$base/shoal-darwin-amd64.tar.gz"
      sha256 "$darwin_amd64"
    end
  end

  on_linux do
    on_arm do
      url "$base/shoal-linux-arm64.tar.gz"
      sha256 "$linux_arm64"
    end
    on_intel do
      url "$base/shoal-linux-amd64.tar.gz"
      sha256 "$linux_amd64"
    end
  end

  def install
    bin.install "shoal"
    prefix.install "THIRD_PARTY_NOTICES.md"
  end

  def caveats
    <<~TEXT
      Shoal needs full network access to sweep the network with ARP. The
      first time it runs, it says what that takes here and asks for your
      password once. To do it now:
        shoal --setup
      On Linux the access belongs to the program file, so after each
      brew upgrade shoal asks once more.
    TEXT
  end

  test do
    assert_match "shoal v#{version}", shell_output("#{bin}/shoal version")
  end
end
EOF
