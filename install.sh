#!/bin/sh
# Shoal installer for macOS, Linux and FreeBSD.
#
#   curl -fsSL https://github.com/BT10011/shoal/releases/latest/download/install.sh | sh
#
# To read it before it runs, and check where it came from, download it
# first:
#
#   curl -fsSLO https://github.com/BT10011/shoal/releases/latest/download/install.sh
#   gh attestation verify install.sh --repo BT10011/shoal
#   less install.sh
#   sh install.sh
#
# It downloads the build for this machine and checks it against the
# release's published checksums. When the GitHub CLI (gh) is installed and
# signed in, it also checks that shoal's release workflow built it. Then it
# installs it as `shoal` on your PATH and runs `shoal --setup`, which says
# what full network access takes on this system and asks for your password
# once. Nothing is installed if a check fails.
#
# Package managers do the same job: Homebrew (brew install
# bt10011/tap/shoal), the .deb, .rpm and Arch packages on the release
# page, or on Windows, the zip on the release page. See the README.
#
# Optional settings, as environment variables before `sh`:
#   SHOAL_VERSION=v2.0.0          a particular release (default: the latest)
#   SHOAL_INSTALL_DIR=~/.local/bin where to put it (default: /usr/local/bin,
#                                 already on your PATH; needs your password)
#   SHOAL_NO_SUDO=1               never use sudo; say what needs it instead
#   SHOAL_BASE_URL=...            download from a mirror instead of GitHub
#   SHOAL_SKIP_PROVENANCE=1       skip the gh check, for a release older than
#                                 v2.0.0, which has no record of its build
#
# Plain POSIX sh on purpose: it runs under the sh of every system it
# supports. Everything happens in main, called on the last line, so a
# download cut short runs nothing.

set -eu

repo="BT10011/shoal"

say() { printf '%s\n' "$*"; }
warn() { printf 'Shoal installer: %s\n' "$*" >&2; }
die() {
	warn "$*"
	exit 1
}

# Run a command as root when it needs to be, asking once through sudo.
as_root() {
	if [ "$(id -u)" = 0 ]; then
		"$@"
	else
		sudo "$@"
	fi
}

main() {
	base="${SHOAL_BASE_URL:-https://github.com/$repo/releases}"
	version="${SHOAL_VERSION:-latest}"
	dir="${SHOAL_INSTALL_DIR:-/usr/local/bin}"
	no_sudo="${SHOAL_NO_SUDO:-}"
	can_sudo() { [ "$(id -u)" = 0 ] || { [ -z "$no_sudo" ] && command -v sudo >/dev/null 2>&1; }; }

	# --- which build ---------------------------------------------------------

	os=$(uname -s)
	arch=$(uname -m)
	case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	FreeBSD) os=freebsd ;;
	MINGW* | MSYS* | CYGWIN*) die "on Windows, take the zip from the release page: https://github.com/$repo/releases/latest" ;;
	*) die "there is no Shoal build for $os" ;;
	esac
	case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "there is no Shoal build for $arch processors" ;;
	esac
	if [ "$os/$arch" = "freebsd/arm64" ]; then
		die "there is no Shoal build for FreeBSD on arm64 yet"
	fi

	asset="shoal-$os-$arch.tar.gz"
	if [ "$version" = latest ]; then
		url="$base/latest/download"
	else
		url="$base/download/$version"
	fi

	# --- tools -----------------------------------------------------------------

	if command -v curl >/dev/null 2>&1; then
		fetch() { curl -fsSL "$1" -o "$2"; }
	elif command -v wget >/dev/null 2>&1; then
		fetch() { wget -q "$1" -O "$2"; }
	else
		die "needs curl or wget to download Shoal"
	fi

	if command -v sha256sum >/dev/null 2>&1; then
		checksum() { sha256sum "$1" | cut -d ' ' -f 1; }
	elif command -v shasum >/dev/null 2>&1; then
		checksum() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
	elif command -v sha256 >/dev/null 2>&1; then
		checksum() { sha256 -q "$1"; }
	else
		die "needs sha256sum, shasum or sha256 to check the download"
	fi

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	# --- download and check ----------------------------------------------------

	say "Downloading Shoal ($version, $os/$arch)..."
	fetch "$url/$asset" "$tmp/$asset" || die "could not download $url/$asset"
	fetch "$url/SHA256SUMS" "$tmp/SHA256SUMS" || die "could not download $url/SHA256SUMS"

	want=$(grep " $asset\$" "$tmp/SHA256SUMS" | cut -d ' ' -f 1)
	[ -n "$want" ] || die "$asset is not listed in the release's SHA256SUMS"
	got=$(checksum "$tmp/$asset")
	[ "$got" = "$want" ] || die "the download of $asset does not match its published checksum, so it is damaged or has been altered; nothing was installed"
	say "Checksum verified."

	# A checksum from the same place as the download shows it arrived
	# whole; the attestation shows shoal's release workflow built it.
	if [ -n "${SHOAL_SKIP_PROVENANCE:-}" ]; then
		say "Provenance not checked (SHOAL_SKIP_PROVENANCE)."
	elif command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
		gh attestation verify "$tmp/$asset" --repo "$repo" >/dev/null ||
			die "GitHub has no record of $repo's release workflow building this $asset, so nothing was installed. (Releases before v2.0.0 have none: SHOAL_SKIP_PROVENANCE=1 installs one anyway.)"
		say "Provenance verified: built by $repo's release workflow."
	else
		say "To also check who built it, install the GitHub CLI, sign in, and run this again."
	fi

	tar -xzf "$tmp/$asset" -C "$tmp"
	bin=""
	for f in "$tmp"/*/shoal; do
		[ -f "$f" ] && bin=$f
	done
	[ -n "$bin" ] || die "the archive holds no shoal binary"

	# --- install ---------------------------------------------------------------

	if mkdir -p "$dir" 2>/dev/null && [ -w "$dir" ]; then
		install -m 0755 "$bin" "$dir/shoal"
	elif can_sudo; then
		say "Installing to $dir needs administrator rights: sudo may ask for your password."
		as_root mkdir -p "$dir"
		as_root install -m 0755 "$bin" "$dir/shoal"
	else
		die "$dir is not writable. Run again with SHOAL_INSTALL_DIR=\$HOME/.local/bin to install without administrator rights."
	fi
	say "Installed $dir/shoal."

	# A binary fetched by a browser carries macOS's quarantine flag; one
	# fetched by curl does not, but clearing it is harmless either way.
	if [ "$os" = darwin ] && command -v xattr >/dev/null 2>&1; then
		xattr -d com.apple.quarantine "$dir/shoal" 2>/dev/null || true
	fi

	# --- PATH ------------------------------------------------------------------

	case ":$PATH:" in
	*":$dir:"*) ;;
	*)
		shell_name=$(basename "${SHELL:-sh}")
		case "$shell_name" in
		zsh)
			rc="$HOME/.zshrc"
			line="export PATH=\"$dir:\$PATH\""
			;;
		bash)
			rc="$HOME/.bashrc"
			[ "$os" = darwin ] && rc="$HOME/.bash_profile"
			line="export PATH=\"$dir:\$PATH\""
			;;
		fish)
			rc="$HOME/.config/fish/config.fish"
			line="fish_add_path $dir"
			;;
		*)
			rc="$HOME/.profile"
			line="export PATH=\"$dir:\$PATH\""
			;;
		esac
		if ! grep -qsF "$line" "$rc"; then
			mkdir -p "$(dirname "$rc")"
			printf '\n# Added by the Shoal installer\n%s\n' "$line" >>"$rc"
			say "Added $dir to your PATH in $rc."
		fi
		say "Open a new terminal, or run this in the current one, before typing shoal:"
		say "  $line"
		;;
	esac

	say ""
	say "$("$dir/shoal" version)"

	# --- full network access ---------------------------------------------------

	# shoal --setup says what it will change and asks first. It reads its
	# answer from the terminal, since this script's own input may be the
	# pipe from curl.
	say ""
	if [ "$os" = freebsd ]; then
		say "On FreeBSD, run Shoal as root: sudo shoal"
	elif [ -n "$no_sudo" ]; then
		say "Shoal needs full network access to scan. When you are ready, run: shoal --setup"
	elif (: </dev/tty) 2>/dev/null; then
		"$dir/shoal" --setup </dev/tty || say "Shoal will offer the same again the next time it starts, or run: shoal --setup"
	else
		say "Shoal needs full network access to scan. In a terminal, run: shoal --setup"
	fi

	# --- done ------------------------------------------------------------------

	say ""
	say "Run it from anywhere:"
	say "  shoal              scan the network you are plugged into"
	say "  shoal --demo       a scripted network, to look around first"
	say "  shoal --upgrade    later, to move to the newest release"
	say "  shoal --uninstall  to remove Shoal, its settings and its history"
}

main "$@"
