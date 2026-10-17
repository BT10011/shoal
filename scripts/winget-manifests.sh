#!/bin/sh
# Writes shoal's winget manifests for a release into a directory, for the
# winget workflow to submit to microsoft/winget-pkgs as
# manifests/b/BT10011/Shoal/<version>/:
#
#   scripts/winget-manifests.sh v2.0.0 dist/SHA256SUMS 2026-10-18 winget
#
# The package is the release's own Windows zip, installed as a portable
# command, each architecture pinned to the checksum in SHA256SUMS. On
# Windows, `winget validate --manifest <dir>` checks the result and
# `winget install --manifest <dir>` installs from it.
set -eu

if [ $# -ne 4 ]; then
	echo "usage: $0 <tag> <SHA256SUMS> <release date, YYYY-MM-DD> <directory>" >&2
	exit 2
fi
tag=$1
sums=$2
date=$3
out=$4
case "$tag" in
v[0-9]*-*)
	echo "winget-manifests: $tag is a pre-release; winget takes releases only" >&2
	exit 1
	;;
v[0-9]*) ;;
*)
	echo "winget-manifests: $tag is not a version tag" >&2
	exit 1
	;;
esac
case "$date" in
[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]) ;;
*)
	echo "winget-manifests: $date is not a date (YYYY-MM-DD)" >&2
	exit 1
	;;
esac
version=${tag#v}
id=BT10011.Shoal
schema=1.12.0
base="https://github.com/BT10011/shoal/releases/download/$tag"

sum() {
	s=$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1 }' "$sums")
	case "$s" in
	[0-9a-f]*) printf '%s' "$s" ;;
	*)
		echo "winget-manifests: $1 is not listed in $sums" >&2
		exit 1
		;;
	esac
}
amd64=$(sum shoal-windows-amd64.zip)
arm64=$(sum shoal-windows-arm64.zip)

mkdir -p "$out"
header="# Written for each release by shoal's winget workflow
# (scripts/winget-manifests.sh in BT10011/shoal)."

cat >"$out/$id.yaml" <<EOF
$header
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.version.$schema.schema.json
PackageIdentifier: $id
PackageVersion: $version
DefaultLocale: en-US
ManifestType: version
ManifestVersion: $schema
EOF

# The folder inside each zip is named for the build, as the release
# contract has it, so the path to shoal.exe differs per architecture.
cat >"$out/$id.installer.yaml" <<EOF
$header
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.installer.$schema.schema.json
PackageIdentifier: $id
PackageVersion: $version
InstallerLocale: en-US
InstallerType: zip
ReleaseDate: "$date"
Installers:
  - Architecture: x64
    NestedInstallerType: portable
    NestedInstallerFiles:
      - RelativeFilePath: shoal-$tag-windows-amd64\\shoal.exe
        PortableCommandAlias: shoal
    InstallerUrl: $base/shoal-windows-amd64.zip
    InstallerSha256: $amd64
    UpgradeBehavior: uninstallPrevious
  - Architecture: arm64
    NestedInstallerType: portable
    NestedInstallerFiles:
      - RelativeFilePath: shoal-$tag-windows-arm64\\shoal.exe
        PortableCommandAlias: shoal
    InstallerUrl: $base/shoal-windows-arm64.zip
    InstallerSha256: $arm64
    UpgradeBehavior: uninstallPrevious
ManifestType: installer
ManifestVersion: $schema
EOF

cat >"$out/$id.locale.en-US.yaml" <<EOF
$header
# yaml-language-server: \$schema=https://aka.ms/winget-manifest.defaultLocale.$schema.schema.json
PackageIdentifier: $id
PackageVersion: $version
PackageLocale: en-US
Publisher: BT10011
PublisherUrl: https://github.com/BT10011
PublisherSupportUrl: https://github.com/BT10011/shoal/issues
Author: BT10011
PackageName: Shoal
PackageUrl: https://github.com/BT10011/shoal
License: MIT
LicenseUrl: https://github.com/BT10011/shoal/blob/$tag/LICENSE
ShortDescription: LAN discovery in the terminal that shows how every device was found
Description: |-
  Shoal finds the devices on the local network with ARP, names them with
  DNS, mDNS and NetBIOS, and shows where every value on screen came from.
Moniker: shoal
Tags:
  - network
  - lan
  - discovery
  - arp
  - mdns
  - terminal
ReleaseNotesUrl: https://github.com/BT10011/shoal/releases/tag/$tag
InstallationNotes: 'Shoal sends ARP through Npcap, which its licence does not allow to be bundled: install it from https://npcap.com, or run shoal --setup to be walked through it.'
ManifestType: defaultLocale
ManifestVersion: $schema
EOF
