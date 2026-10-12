//go:build windows

package neigh

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TableSource names where the entries come from, for the UI and docs.
const TableSource = "the IPv4 neighbour table (GetIpNetTable)"

var procGetIpNetTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetIpNetTable")

// Table reads Windows' IPv4 neighbour cache, the table `arp -a` prints.
func Table() ([]Entry, error) {
	// Ask for the size first, then grow to what the call says it needs:
	// the table can grow between calls while a nudge is being answered.
	var (
		buf  []byte
		size uint32
	)
	for range 4 {
		var p uintptr
		if size > 0 {
			buf = make([]byte, size)
			p = uintptr(unsafe.Pointer(&buf[0]))
		}
		r, _, _ := procGetIpNetTable.Call(p, uintptr(unsafe.Pointer(&size)), 0)
		switch err := syscall.Errno(r); err {
		case windows.ERROR_SUCCESS:
			return parseIPNetTable(buf, interfaceNames())
		case windows.ERROR_NO_DATA:
			return nil, nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			continue
		default:
			return nil, fmt.Errorf("GetIpNetTable: %w", err)
		}
	}
	return nil, errors.New("GetIpNetTable: the neighbour table kept growing")
}

// interfaceNames names interfaces by index, as net.Interfaces does, asking
// once per index: on Windows each lookup reads the whole adapter table.
func interfaceNames() func(uint32) string {
	names := make(map[uint32]string)
	return func(index uint32) string {
		name, ok := names[index]
		if !ok {
			if ni, err := net.InterfaceByIndex(int(index)); err == nil {
				name = ni.Name
			}
			names[index] = name
		}
		return name
	}
}
