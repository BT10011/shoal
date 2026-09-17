package neigh

import "os"

// TableSource names where the entries come from, for the UI and docs.
const TableSource = "/proc/net/arp"

// Table reads the kernel's IPv4 neighbour cache.
func Table() ([]Entry, error) {
	f, err := os.Open(procARPPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseProcARP(f)
}

const procARPPath = "/proc/net/arp"
