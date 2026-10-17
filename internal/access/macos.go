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

// macSetup is what setup runs as root on macOS, with the user to add as $1.
// Each step is skipped when it is already done, so it is safe to run again.
// A new group takes the lowest free ID from 100 up, as Wireshark's
// installer chooses, which keeps it out of the range macOS shows as user
// groups. The membership cache is flushed at the end so the kernel asks
// about the new member rather than remembering the old answer.
const macSetup = `set -e
PATH=/usr/sbin:/usr/bin:/sbin:/bin
user="$1"
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
	launchctl bootout system/` + DaemonLabel + ` 2>/dev/null || true
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
