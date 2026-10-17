//go:build windows

package arp

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/BT10011/shoal/internal/netif"
)

// Windows has no raw link-layer socket, for administrators or anyone else,
// so raw ARP goes through Npcap's packet driver and its libpcap, wpcap.dll.
// The DLL is loaded when raw access is first wanted rather than linked:
// without Npcap shoal still starts, and offers to set it up (internal/access).

// wpcap holds the functions shoal calls, found once.
var wpcap struct {
	once sync.Once
	err  error

	openLive, setFilter, nextEx, sendPacket, getErr, close *windows.Proc
	setMinToCopy                                           *windows.Proc // nil if this wpcap lacks it
}

// loadWpcap finds wpcap.dll where Npcap installs it, System32\Npcap, and
// then in System32 itself, where Npcap's WinPcap-compatible mode and old
// WinPcap installs put it. Only those two places are searched, never the
// program's folder or the current one, so a stray wpcap.dll beside
// shoal.exe is never loaded. Packet.dll, which wpcap.dll needs, is found
// in the same folder.
func loadWpcap() error {
	wpcap.once.Do(func() {
		sys, err := windows.GetSystemDirectory()
		if err != nil {
			wpcap.err = fmt.Errorf("finding the system folder: %w", err)
			return
		}
		var h windows.Handle
		for _, path := range []string{filepath.Join(sys, "Npcap", "wpcap.dll"), filepath.Join(sys, "wpcap.dll")} {
			h, err = windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
			if err == nil {
				break
			}
		}
		if err != nil {
			wpcap.err = ErrNoNpcap
			return
		}
		dll := &windows.DLL{Name: "wpcap.dll", Handle: h}
		for name, p := range map[string]**windows.Proc{
			"pcap_open_live":  &wpcap.openLive,
			"pcap_setfilter":  &wpcap.setFilter,
			"pcap_next_ex":    &wpcap.nextEx,
			"pcap_sendpacket": &wpcap.sendPacket,
			"pcap_geterr":     &wpcap.getErr,
			"pcap_close":      &wpcap.close,
		} {
			if *p, err = dll.FindProc(name); err != nil {
				wpcap.err = fmt.Errorf("wpcap.dll has no %s; Npcap may be damaged, so install it again from %s: %w", name, npcapURL, err)
				return
			}
		}
		wpcap.setMinToCopy, _ = dll.FindProc("pcap_setmintocopy")
	})
	return wpcap.err
}

// npcapAdminOnly reports whether Npcap was installed with access limited
// to administrators, which its installer records in the registry.
func npcapAdminOnly() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\npcap\Parameters`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AdminOnly")
	return err == nil && v != 0
}

// OpenConn opens two Npcap handles on the interface: one filtered to ARP
// for reading, one that keeps nothing for sending.
func OpenConn(iface netif.Interface) (Conn, error) {
	if err := loadWpcap(); err != nil {
		return nil, err
	}
	// Checked before opening anything: with access limited to
	// administrators, Npcap answers an ordinary user's open with a Windows
	// prompt asking for elevation, which would greet every start.
	if npcapAdminOnly() && !windows.GetCurrentProcessToken().IsElevated() {
		return nil, ErrNpcapAdminOnly
	}
	guid, err := netif.AdapterGUID(iface.Index)
	if err != nil {
		return nil, fmt.Errorf("finding %s's Npcap device: %w", iface.Name, err)
	}
	device := npfDevice(guid)
	recv, err := openWpcap(device, arpOnly)
	if err != nil {
		return nil, fmt.Errorf("%s (%s): %w", iface.Name, device, err)
	}
	send, err := openWpcap(device, keepNothing)
	if err != nil {
		recv.close()
		return nil, fmt.Errorf("%s (%s): %w", iface.Name, device, err)
	}
	return newPcapConn(recv, send, iface.MAC), nil
}

// bpfInsn and bpfProgram are libpcap's struct bpf_insn and bpf_program.
type bpfInsn struct {
	Code   uint16
	Jt, Jf uint8
	K      uint32
}

type bpfProgram struct {
	Len   uint32
	Insns *bpfInsn
}

// arpOnly accepts frames whose Ethernet type is ARP and drops the rest:
// the same program the BPF devices run on macOS and FreeBSD. Npcap runs it
// in the driver.
var arpOnly = []bpfInsn{
	{Code: 0x28, K: 12},                   // ldh [12]           load ethertype
	{Code: 0x15, Jt: 0, Jf: 1, K: 0x0806}, // jeq #0x0806, L1, L2
	{Code: 0x06, K: 0xffffffff},           // L1: ret #-1        accept whole frame
	{Code: 0x06, K: 0},                    // L2: ret #0         drop
}

// keepNothing drops every frame, so the sending handle does not fill its
// buffer with traffic nobody reads.
var keepNothing = []bpfInsn{
	{Code: 0x06, K: 0}, // ret #0
}

const (
	snapLen     = 2048 // an ARP frame is 42 to 64 bytes
	readTimeout = 100  // milliseconds: how often a read wakes to notice Close
	errbufSize  = 256  // PCAP_ERRBUF_SIZE
)

// wpcapHandle is one pcap_t.
type wpcapHandle struct {
	p uintptr
}

// openWpcap opens a handle on the device, not promiscuous (as on Linux and
// macOS: only broadcasts and frames for this machine), and installs the
// filter.
func openWpcap(device string, filter []bpfInsn) (*wpcapHandle, error) {
	name, err := windows.BytePtrFromString(device)
	if err != nil {
		return nil, err
	}
	errbuf := make([]byte, errbufSize)
	p, _, _ := wpcap.openLive.Call(uintptr(unsafe.Pointer(name)), snapLen, 0, readTimeout, uintptr(unsafe.Pointer(&errbuf[0])))
	if p == 0 {
		return nil, fmt.Errorf("pcap_open_live: %s", windows.ByteSliceToString(errbuf))
	}
	h := &wpcapHandle{p: p}
	// Hand each frame over as soon as it arrives, not once the driver holds
	// 16 KB of them (its default), so a reply is timed when it came.
	if wpcap.setMinToCopy != nil {
		wpcap.setMinToCopy.Call(h.p, 0)
	}
	prog := bpfProgram{Len: uint32(len(filter)), Insns: &filter[0]}
	if r, _, _ := wpcap.setFilter.Call(h.p, uintptr(unsafe.Pointer(&prog))); int32(r) != 0 {
		err := fmt.Errorf("pcap_setfilter: %s", h.lastError())
		h.close()
		return nil, err
	}
	return h, nil
}

// pcapPkthdr is struct pcap_pkthdr on Windows, where a timeval holds two
// 32-bit longs.
type pcapPkthdr struct {
	Sec, Usec int32
	Caplen    uint32
	Len       uint32
}

func (h *wpcapHandle) next() ([]byte, error) {
	var (
		hdr  *pcapPkthdr
		data *byte
	)
	r, _, _ := wpcap.nextEx.Call(h.p, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data)))
	switch int32(r) {
	case 1:
		if hdr == nil || data == nil {
			return nil, nil
		}
		return unsafe.Slice(data, hdr.Caplen), nil
	case 0: // the read timeout passed
		return nil, nil
	default:
		return nil, fmt.Errorf("pcap_next_ex: %s", h.lastError())
	}
}

func (h *wpcapHandle) send(frame []byte) error {
	if len(frame) == 0 {
		return errors.New("empty frame")
	}
	if r, _, _ := wpcap.sendPacket.Call(h.p, uintptr(unsafe.Pointer(&frame[0])), uintptr(len(frame))); int32(r) != 0 {
		return fmt.Errorf("pcap_sendpacket: %s", h.lastError())
	}
	return nil
}

func (h *wpcapHandle) close() {
	wpcap.close.Call(h.p)
}

// lastError is pcap_geterr: the handle's last error, as text.
func (h *wpcapHandle) lastError() string {
	r, _, _ := wpcap.getErr.Call(h.p)
	if r == 0 {
		return "unknown error"
	}
	return windows.BytePtrToString(*(**byte)(unsafe.Pointer(&r)))
}
