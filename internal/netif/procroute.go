package netif

import (
	"bufio"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
)

const procRTFGateway = 0x2

// parseProcRoute reads Linux /proc/net/route and returns the lowest-metric
// IPv4 default route. Addresses in that file are little-endian hex.
func parseProcRoute(r io.Reader) (gateway net.IP, ifName string, err error) {
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		return nil, "", errors.New("/proc/net/route is empty")
	}
	bestMetric := -1
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 {
			continue
		}
		if fields[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&procRTFGateway == 0 {
			continue
		}
		metric, err := strconv.Atoi(fields[6])
		if err != nil {
			continue
		}
		gw, err := hexLittleEndianIP(fields[2])
		if err != nil {
			continue
		}
		if bestMetric == -1 || metric < bestMetric {
			bestMetric = metric
			gateway, ifName = gw, fields[0]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, "", err
	}
	if gateway == nil {
		return nil, "", errors.New("no IPv4 default route in /proc/net/route")
	}
	return gateway, ifName, nil
}

func hexLittleEndianIP(s string) (net.IP, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != 4 {
		return nil, errors.New("address is not 4 bytes")
	}
	return net.IPv4(b[3], b[2], b[1], b[0]).To4(), nil
}
