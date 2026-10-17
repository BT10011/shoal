package access

// What setup installs on macOS. These live in a file every platform
// builds, so the scripts are syntax-checked by the tests wherever they run
// and `shoal --uninstall` can name the files.

const (
	// Group is the group whose members may use the packet capture
	// devices. Wireshark's ChmodBPF uses the same name, so the two share it.
	Group = "access_bpf"
	// DaemonLabel names the start-up job that opens the devices to Group.
	DaemonLabel = "io.github.bt10011.shoal.chmodbpf"
	// DaemonPlist is the start-up job's definition.
	DaemonPlist = "/Library/LaunchDaemons/" + DaemonLabel + ".plist"
	// SupportDir holds the script the start-up job runs.
	SupportDir = "/Library/Application Support/Shoal"
	// ChmodScript is that script.
	ChmodScript = SupportDir + "/chmod-bpf"
	// WiresharkPlist is Wireshark's start-up job for the same thing. When
	// it is installed, shoal installs no job of its own.
	WiresharkPlist = "/Library/LaunchDaemons/org.wireshark.ChmodBPF.plist"
	// DataFolder is where shoal keeps its settings and history on macOS,
	// under the home folder: the folder config.DefaultPath and
	// store.DefaultHistoryPath both use.
	DataFolder = "Library/Application Support/shoal"
)

// openDevices opens the packet capture devices to Group. macOS makes the
// next BPF device only when the last one is opened, so each is opened in
// turn, up to the kernel's limit, before all of them are changed. A device
// that cannot be opened is skipped without stopping a script run with
// set -e.
const openDevices = `max=$(sysctl -n debug.bpf_maxdevices 2>/dev/null)
case "$max" in '' | *[!0-9]*) max=256 ;; esac
[ "$max" -le 256 ] || max=256
i=0
while [ "$i" -lt "$max" ]; do
	{ true <"/dev/bpf$i"; } 2>/dev/null || true
	i=$((i + 1))
done
chgrp ` + Group + ` /dev/bpf*
chmod g+rw /dev/bpf*
`

// chmodScript is ChmodScript's contents.
const chmodScript = `#!/bin/sh
# Opens the packet capture devices, /dev/bpf*, to the ` + Group + ` group, so
# its members can send and capture raw packets without root. Installed by
# shoal --setup, run at every start-up by
# ` + DaemonPlist + `,
# and removed by shoal --uninstall. Wireshark's ChmodBPF does the same.
PATH=/usr/sbin:/usr/bin:/sbin:/bin
` + openDevices

// daemonPlist is DaemonPlist's contents: run ChmodScript when loaded,
// which is at every start-up.
const daemonPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + DaemonLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + ChmodScript + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`

// freeGID reads "name id" lines, as dscl lists groups, and prints the
// lowest group ID from 100 up that none of them has.
const freeGID = `awk '{ print $2 }' | sort -n | awk 'BEGIN { g = 100 } $1 == g { g++ } END { print g }'`

// giveBack defines give_back, which hands $user back the folder of
// settings and history that shoal run with sudo left belonging to root.
// v1.0 told Mac users to run sudo shoal, and macOS's sudo keeps HOME, so
// root made the folder, and only root can read it. give_back runs as root
// on a folder inside one the account can write to, so it takes care to
// change nothing else:
//
//   - the folder is moved into a new folder in /private/tmp that only root
//     can reach, and that /private/tmp's sticky bit stops anyone else from
//     renaming, so nothing can be swapped in once it has been checked;
//   - it is changed only if root owns it and nobody else may write to it,
//     so everything inside was put there by root: no one could have
//     planted a hard link to a system file in it;
//   - what is inside is changed before the folder itself, with chown -h,
//     which never follows a symbolic link.
//
// The folder always goes back where it was. A failure is reported without
// stopping setup.
const giveBack = `give_back() {
	home=$(id -P "$user" | cut -d: -f9)
	data="$home/` + DataFolder + `"
	[ -n "$home" ] && [ -d "$data" ] && [ ! -L "$data" ] || return 0
	hold=$(mktemp -d /private/tmp/shoal-setup.XXXXXX) || return 1
	if ! mv "$data" "$hold/shoal"; then
		rmdir "$hold"
		return 1
	fi
	status=0
	if [ -n "$(find "$hold/shoal" -maxdepth 0 -type d -user root ! -perm -g+w ! -perm -o+w)" ]; then
		find "$hold/shoal" -depth -exec chown -h "$user" {} + || status=1
	fi
	back="$data"
	[ ! -e "$back" ] || back="$data.from-sudo"
	if ! mv "$hold/shoal" "$back"; then
		echo "shoal: your settings and history are in $hold/shoal; move them back to $data" >&2
		return 1
	fi
	rmdir "$hold"
	return "$status"
}
`

// macSetup is what setup runs as root on macOS, with the user to add as $1,
// and 1 as $2 to give back the folder of settings and history (giveBack).
// Each step is skipped when it is already done, so it is safe to run again:
// the start-up job's files are written afresh, and the job is loaded only
// if it is not loaded already, since unloading and at once loading a job
// can fail while launchd is still taking it down. Its definition never
// changes, so the loaded one is the one just written. A new group takes the
// lowest free ID from 100 up, as Wireshark's
// installer chooses, which keeps it out of the range macOS shows as user
// groups. The membership cache is flushed at the end so the kernel asks
// about the new member rather than remembering the old answer.
const macSetup = `set -e
PATH=/usr/sbin:/usr/bin:/sbin:/bin
user="$1"
` + giveBack + `if [ "$2" = 1 ]; then
	give_back || echo "shoal: could not give $user back ~/` + DataFolder + `; to do it yourself, run: sudo chown -R $user ~/'` + DataFolder + `'" >&2
fi
if ! dscl . -read "/Groups/` + Group + `" >/dev/null 2>&1; then
	gid=$(dscl . -list /Groups PrimaryGroupID | ` + freeGID + `)
	dseditgroup -q -o create -i "$gid" -r "BPF device access" ` + Group + `
fi
dseditgroup -q -o edit -a "$user" -t user ` + Group + `
if [ ! -f "` + WiresharkPlist + `" ]; then
	mkdir -p "` + SupportDir + `"
	cat >"` + ChmodScript + `" <<'SHOAL_SCRIPT'
` + chmodScript + `SHOAL_SCRIPT
	chown root:wheel "` + ChmodScript + `"
	chmod 755 "` + ChmodScript + `"
	cat >"` + DaemonPlist + `" <<'SHOAL_PLIST'
` + daemonPlist + `SHOAL_PLIST
	chown root:wheel "` + DaemonPlist + `"
	chmod 644 "` + DaemonPlist + `"
	launchctl print system/` + DaemonLabel + ` >/dev/null 2>&1 ||
		launchctl bootstrap system "` + DaemonPlist + `"
fi
` + openDevices + `dsmemberutil flushcache
`

// macRemove is what --uninstall runs as root on macOS: the start-up job and
// its script go, and the group stays, since Wireshark may use it too.
const macRemove = `PATH=/usr/sbin:/usr/bin:/sbin:/bin
launchctl bootout system/` + DaemonLabel + ` 2>/dev/null || true
rm -f "` + DaemonPlist + `"
rm -rf "` + SupportDir + `"
`
