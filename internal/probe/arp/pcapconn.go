package arp

import (
	"bytes"
	"errors"
	"net"
	"sync"
)

// pcapHandle is one open capture handle: what the Npcap connection needs
// of wpcap, kept apart from the DLL so the connection is tested on every
// platform.
type pcapHandle interface {
	// next returns the next frame that passed the handle's filter, or
	// (nil, nil) when the read timeout passed with none. The frame is
	// valid only until the next call.
	next() ([]byte, error)
	send(frame []byte) error
	close()
}

// pcapConn is a raw link-layer connection over two capture handles, one
// for receiving and one for sending. A pcap handle must not be used from
// two goroutines at once, and the listener reads while the sweep sends.
// Each handle has its own lock, and Close takes both, so a handle is never
// closed under a call still using it.
type pcapConn struct {
	own net.HardwareAddr // this interface's MAC: frames from it are ours

	rmu  sync.Mutex
	recv pcapHandle

	smu  sync.Mutex
	send pcapHandle

	closed bool // written holding both locks, read holding either
}

func newPcapConn(recv, send pcapHandle, own net.HardwareAddr) *pcapConn {
	return &pcapConn{recv: recv, send: send, own: own}
}

func (c *pcapConn) WriteFrame(frame []byte) error {
	c.smu.Lock()
	defer c.smu.Unlock()
	if c.closed {
		return errors.New("npcap: closed")
	}
	return c.send.send(frame)
}

// ReadFrame returns the next ARP frame, or (nil, nil) when none arrived
// within the read timeout or the frame was one this machine sent. Npcap
// hands a frame sent on one handle to every other handle on the adapter,
// so the receive handle sees the sweep's own requests; Linux and the BPF
// devices leave those out themselves.
func (c *pcapConn) ReadFrame() ([]byte, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if c.closed {
		return nil, errors.New("npcap: closed")
	}
	frame, err := c.recv.next()
	if err != nil || frame == nil {
		return nil, err
	}
	if len(c.own) == 6 && len(frame) >= 12 && bytes.Equal(frame[6:12], c.own) {
		return nil, nil
	}
	return append([]byte(nil), frame...), nil
}

func (c *pcapConn) Close() error {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	c.smu.Lock()
	defer c.smu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.recv.close()
	c.send.close()
	return nil
}

// npfDevice is the name Npcap gives an adapter's capture device.
func npfDevice(guid string) string {
	return `\Device\NPF_` + guid
}

// npcapURL is where Npcap is downloaded from. Its licence does not allow
// shoal to ship it, so the user installs it once.
const npcapURL = "https://npcap.com"

// npcapRestrictOption is the installer's wording for the option that
// limits Npcap to administrators, so the advice names the box to look for.
const npcapRestrictOption = `"Restrict Npcap driver's access to Administrators only"`

var (
	// ErrNoNpcap: wpcap.dll is in neither of the places Npcap installs it.
	ErrNoNpcap = &AccessError{
		Reason: "no Npcap",
		Advice: "Npcap is not installed. Windows has no raw network access of its own, so shoal sends and hears ARP through Npcap. " +
			"Install it from " + npcapURL + " with " + npcapRestrictOption + " left unticked, then start shoal again; " +
			"shoal --setup walks you through it. No administrator rights are needed after that.",
	}
	// ErrNpcapAdminOnly: Npcap is installed for administrators only and
	// shoal is not running as one. Opening the adapter anyway would make
	// Npcap ask for elevation through a Windows prompt every time shoal
	// started.
	ErrNpcapAdminOnly = &AccessError{
		Reason: "Npcap is for administrators only",
		Advice: "Npcap is installed with " + npcapRestrictOption + " ticked, and shoal is not running as an administrator. " +
			"Install Npcap again from " + npcapURL + " with that box unticked, then start shoal again; " +
			"shoal --setup walks you through it.",
	}
)
