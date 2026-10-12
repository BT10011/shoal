//go:build windows

package icmp

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho    = iphlpapi.NewProc("IcmpSendEcho")
)

// Listen returns Windows' echo API, IcmpSendEcho, presented as a socket.
// Windows has no unprivileged ICMP socket and a raw one needs an
// administrator; the API is what ping.exe uses, and needs neither.
func Listen(context.Context) (net.PacketConn, Mode, error) {
	if err := procIcmpSendEcho.Find(); err != nil {
		return nil, "", fmt.Errorf("no ICMP: Windows' echo API is missing: %w", err)
	}
	return newEchoConn(prepareEcho), ModeEchoAPI, nil
}

// Statuses IcmpSendEcho reports (ipexport.h).
const (
	ipSuccess             = 0
	ipDestNetUnreachable  = 11002
	ipDestHostUnreachable = 11003
	ipDestProtUnreachable = 11004
	ipDestPortUnreachable = 11005
)

// icmpEchoReply is ICMP_ECHO_REPLY; Go lays it out as C does on both 32-
// and 64-bit Windows.
type icmpEchoReply struct {
	Address       uint32 // network byte order
	Status        uint32
	RoundTripTime uint32 // whole milliseconds; the probe times it itself
	DataSize      uint16
	Reserved      uint16
	Data          uintptr // points into the reply buffer
	Options       struct {
		TTL, TOS, Flags, OptionsSize uint8
		OptionsData                  uintptr
	}
}

// prepareEcho opens a handle for one request. Each request has its own,
// closed when its call returns, so closing the connection never pulls a
// handle out from under a call still waiting for its answer. Measured on
// Windows 11, a fresh handle costs no more than a reused one, and calls on
// one handle do not wait for each other. It is opened here, before the
// probe starts its clock, so opening it is never counted as network time.
func prepareEcho() (echoFunc, error) {
	h, _, err := procIcmpCreateFile.Call()
	if windows.Handle(h) == windows.InvalidHandle {
		return nil, fmt.Errorf("IcmpCreateFile: %w", err)
	}
	return func(dst net.IP, data []byte, timeout time.Duration) echoResult {
		defer procIcmpCloseHandle.Call(h)
		return icmpSendEcho(h, dst, data, timeout)
	}, nil
}

// icmpSendEcho sends one echo request through IcmpSendEcho on handle h and
// waits for its outcome.
func icmpSendEcho(h uintptr, dst net.IP, data []byte, timeout time.Duration) echoResult {

	// Room for one reply, the echoed data and an ICMP error's 8 bytes.
	buf := make([]byte, int(unsafe.Sizeof(icmpEchoReply{}))+len(data)+8+64)
	var reqData uintptr
	if len(data) > 0 {
		reqData = uintptr(unsafe.Pointer(&data[0]))
	}
	n, _, callErr := procIcmpSendEcho.Call(h,
		uintptr(binary.LittleEndian.Uint32(dst.To4())), // IPAddr: the address's bytes as they stand
		reqData, uintptr(len(data)), 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		uintptr(max(timeout.Milliseconds(), 1)))
	if n == 0 {
		// No reply structure: the status is the call's error.
		if isUnreachable(callErr) {
			return echoResult{Outcome: echoUnreachable, From: dst}
		}
		return echoResult{}
	}
	reply := (*icmpEchoReply)(unsafe.Pointer(&buf[0]))
	from := make(net.IP, net.IPv4len)
	binary.LittleEndian.PutUint32(from, reply.Address)
	switch {
	case reply.Status == ipSuccess:
		return echoResult{Outcome: echoReplied, From: from, Data: echoedData(buf, reply)}
	case isUnreachable(syscall.Errno(reply.Status)):
		return echoResult{Outcome: echoUnreachable, From: from}
	default:
		return echoResult{}
	}
}

// echoedData copies the payload the reply carried out of the buffer.
func echoedData(buf []byte, reply *icmpEchoReply) []byte {
	start := reply.Data - uintptr(unsafe.Pointer(&buf[0]))
	end := start + uintptr(reply.DataSize)
	if reply.Data == 0 || start > uintptr(len(buf)) || end > uintptr(len(buf)) {
		return nil
	}
	return append([]byte(nil), buf[start:end]...)
}

func isUnreachable(err error) bool {
	switch err {
	case syscall.Errno(ipDestNetUnreachable), syscall.Errno(ipDestHostUnreachable),
		syscall.Errno(ipDestProtUnreachable), syscall.Errno(ipDestPortUnreachable):
		return true
	}
	return false
}
