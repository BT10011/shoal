#!/bin/sh
# Shoal installer for macOS, Linux and FreeBSD.
#
#   curl -fsSL https://github.com/BT10011/shoal/releases/latest/download/install.sh | sh
#
# It downloads the build for this machine, checks it against the published
# checksums, installs it as `shoal` on your PATH, and on Linux grants it the
# one capability it needs to send ARP without running as root.
#
# Optional settings, as environment variables before `sh`:
#   SHOAL_VERSION=v1.0.0          a particular release (default: the latest)
#   SHOAL_INSTALL_DIR=~/.local/bin where to put it (default: /usr/local/bin,
#                                 already on your PATH; needs your password)
#   SHOAL_NO_SUDO=1               never use sudo; say what needs it instead
#   SHOAL_BASE_URL=...            download from a mirror instead of GitHub
#
# Plain POSIX sh on purpose: it runs under the sh of every system it
# supports.

set -eu

repo="BT10011/shoal"
base="${SHOAL_BASE_URL:-https://github.com/$repo/releases}"
version="${SHOAL_VERSION:-latest}"
dir="${SHOAL_INSTALL_DIR:-/usr/local/bin}"
no_sudo="${SHOAL_NO_SUDO:-}"

say() { printf '%s\n' "$*"; }
warn() { printf 'Shoal installer: %s\n' "$*" >&2; }
die() {
	warn "$*"
	exit 1
}

# --- which build -----------------------------------------------------------

os=$(uname -s)
arch=$(uname -m)
case "$os" in
Linux) os=linux ;;
Darwin) os=darwin ;;
FreeBSD) os=freebsd ;;
*) die "there is no Shoal build for $os yet (a Windows release is planned)" ;;
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

# --- tools -------------------------------------------------------------------

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

# Run a command as root when it needs to be, asking once through sudo.
as_root() {
	if [ "$(id -u)" = 0 ]; then
		"$@"
	else
		sudo "$@"
	fi
}
can_sudo() { [ "$(id -u)" = 0 ] || { [ -z "$no_sudo" ] && command -v sudo >/dev/null 2>&1; }; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

# --- download and check ------------------------------------------------------

say "Downloading Shoal ($version, $os/$arch)..."
fetch "$url/$asset" "$tmp/$asset" || die "could not download $url/$asset"
fetch "$url/SHA256SUMS" "$tmp/SHA256SUMS" || die "could not download $url/SHA256SUMS"

want=$(grep " $asset\$" "$tmp/SHA256SUMS" | cut -d ' ' -f 1)
[ -n "$want" ] || die "$asset is not listed in the release's SHA256SUMS"
got=$(checksum "$tmp/$asset")
[ "$got" = "$want" ] || die "the download of $asset does not match its published checksum, so it is damaged or has been altered; nothing was installed"
say "Checksum verified."

tar -xzf "$tmp/$asset" -C "$tmp"
bin=""
for f in "$tmp"/*/shoal; do
	[ -f "$f" ] && bin=$f
done
[ -n "$bin" ] || die "the archive holds no shoal binary"

# --- install -----------------------------------------------------------------

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

# A binary fetched by a browser carries macOS's quarantine flag; one fetched
# by curl does not, but clearing it is harmless either way.
if [ "$os" = darwin ] && command -v xattr >/dev/null 2>&1; then
	xattr -d com.apple.quarantine "$dir/shoal" 2>/dev/null || true
fi

# --- raw network access ------------------------------------------------------

if [ "$os" = linux ]; then
	setcap_bin=$(command -v setcap 2>/dev/null || true)
	for p in /usr/sbin/setcap /sbin/setcap; do
		[ -z "$setcap_bin" ] && [ -x "$p" ] && setcap_bin=$p
	done
	grant="sudo setcap cap_net_raw+ep $dir/shoal"
	if [ -z "$setcap_bin" ]; then
		warn "setcap is not installed (package libcap2-bin on Debian and Ubuntu, libcap on Fedora and Arch). Until it is, Shoal runs in its reduced mode. Afterwards run: $grant"
	elif can_sudo; then
		say "Granting Shoal raw network access (cap_net_raw) so it can send ARP without running as root."
		as_root "$setcap_bin" cap_net_raw+ep "$dir/shoal" || warn "could not grant raw network access; Shoal will run in its reduced mode. To grant it later: $grant"
	else
		say "To let Shoal send ARP itself, run once: $grant"
	fi
fi

# --- PATH --------------------------------------------------------------------

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

# --- done ----------------------------------------------------------------------

say ""
say "$("$dir/shoal" version)"
say ""
say "Run it from anywhere:"
say "  shoal            scan the network you are plugged into"
say "  shoal --demo     a scripted network, to look around first"
if [ "$os" = darwin ]; then
	say ""
	say "On macOS the raw network devices are root-only: run 'sudo shoal', or"
	say "install Wireshark's ChmodBPF once to use it without sudo. Without either,"
	say "Shoal runs in its reduced mode."
fi
if [ "$os" = freebsd ]; then
	say ""
	say "On FreeBSD run it as root; there is no reduced mode yet."
fi
say ""
say "  shoal --update   later, to move to the newest release"
say "  shoal --uninstall  to remove Shoal, its settings and its history"
