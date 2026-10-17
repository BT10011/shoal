#!/bin/sh
# Run by the .deb, .rpm and Arch packages after installing or upgrading
# shoal. It grants the program file the one capability shoal needs to send
# and hear ARP without running as root, cap_net_raw; a new file has none,
# so every upgrade grants it again. It never fails the install: if the
# capability cannot be granted, shoal offers to when it starts. (It ends
# with : rather than exit, since the Arch package runs it as the body of a
# function.)
setcap=$(command -v setcap 2>/dev/null)
for p in /usr/sbin/setcap /sbin/setcap /usr/bin/setcap; do
	[ -z "$setcap" ] && [ -x "$p" ] && setcap=$p
done
if [ -z "$setcap" ]; then
	echo "shoal: setcap is not installed, so shoal will ask for raw network access when it starts" >&2
elif ! "$setcap" cap_net_raw+ep /usr/bin/shoal; then
	echo "shoal: could not grant cap_net_raw to /usr/bin/shoal; shoal will ask for it when it starts" >&2
fi
:
