// Package nbns asks a host for its NetBIOS name table — the "node status"
// request that nbtscan and `nmblookup -A` send. It is how Windows and Samba
// machines announce themselves, and it names the devices that answer neither
// mDNS nor a reverse DNS lookup.
package nbns

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

// Port is the NetBIOS Name Service port.
const Port = 137

// TypeNBSTAT is the query type that asks for a node's whole name table.
// Class is the same IN (1) that DNS uses, because NBNS borrowed DNS's
// message format wholesale (RFC 1002).
const (
	TypeNBSTAT = 0x0021
	ClassIN    = 0x0001
)

// nameLen is the fixed width of a NetBIOS name: 15 bytes of name, padded with
// spaces, then one byte saying which service it belongs to.
const nameLen = 15

// Wildcard is the name a node status request asks about: "*" padded with
// nulls. It means "whatever you are", which is the point — shoal does not
// know the name yet.
func Wildcard() [16]byte {
	var name [16]byte
	name[0] = '*'
	return name
}

// Encode applies NetBIOS's peculiar name encoding: every byte becomes two
// letters, its high nibble and its low nibble each added to 'A'. It exists
// because NetBIOS names may contain bytes that are not legal in DNS labels,
// and the result is the run of A's and K's visible in any packet capture.
func Encode(name [16]byte) []byte {
	out := make([]byte, 0, 32)
	for _, b := range name {
		out = append(out, 'A'+(b>>4), 'A'+(b&0x0f))
	}
	return out
}

// Decode reverses Encode.
func Decode(encoded []byte) ([16]byte, error) {
	var name [16]byte
	if len(encoded) != 32 {
		return name, fmt.Errorf("a NetBIOS name encodes to 32 bytes, got %d", len(encoded))
	}
	for i := 0; i < 16; i++ {
		hi, lo := encoded[i*2]-'A', encoded[i*2+1]-'A'
		if hi > 0x0f || lo > 0x0f {
			return name, fmt.Errorf("byte %d of the encoded name is not in A..P", i*2)
		}
		name[i] = hi<<4 | lo
	}
	return name, nil
}

// Query builds a node status request.
func Query(txid uint16) []byte {
	msg := make([]byte, 0, 50)
	msg = binary.BigEndian.AppendUint16(msg, txid)
	msg = binary.BigEndian.AppendUint16(msg, 0) // no flags: a plain query
	msg = binary.BigEndian.AppendUint16(msg, 1) // one question
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, 0)

	encoded := Encode(Wildcard())
	msg = append(msg, byte(len(encoded)))
	msg = append(msg, encoded...)
	msg = append(msg, 0) // root label

	msg = binary.BigEndian.AppendUint16(msg, TypeNBSTAT)
	msg = binary.BigEndian.AppendUint16(msg, ClassIN)
	return msg
}

// Name is one entry in a node's name table.
type Name struct {
	Name   string
	Suffix byte
	Group  bool
	Flags  uint16
}

// Service names the suffix byte, which is what the name is *for*. A machine
// registers the same name several times, once per service it offers.
func (n Name) Service() string {
	switch n.Suffix {
	case 0x00:
		if n.Group {
			return "domain or workgroup"
		}
		return "workstation"
	case 0x03:
		return "messenger"
	case 0x06:
		return "RAS server"
	case 0x1b:
		return "domain master browser"
	case 0x1c:
		return "domain controllers"
	case 0x1d:
		return "master browser"
	case 0x1e:
		return "browser elections"
	case 0x20:
		return "file server"
	case 0x21:
		return "RAS client"
	default:
		return fmt.Sprintf("suffix 0x%02x", n.Suffix)
	}
}

// Kind says whether the name belongs to one machine or to a set of them.
func (n Name) Kind() string {
	if n.Group {
		return "group"
	}
	return "unique"
}

func (n Name) String() string {
	return fmt.Sprintf("%s <%02x> %s, %s", n.Name, n.Suffix, n.Service(), n.Kind())
}

// NodeStatus is a decoded name table.
type NodeStatus struct {
	TxID  uint16
	Names []Name
	MAC   net.HardwareAddr // the adapter's own address, as the node reports it
}

// groupFlag is the top bit of a name's flags: set means several machines may
// hold this name, which is how workgroups and domains are represented.
const groupFlag = 0x8000

// ParseNodeStatus decodes a node status response.
func ParseNodeStatus(wire []byte) (NodeStatus, error) {
	if len(wire) < 12 {
		return NodeStatus{}, fmt.Errorf("a NetBIOS reply is at least 12 bytes, got %d", len(wire))
	}
	status := NodeStatus{TxID: binary.BigEndian.Uint16(wire[0:2])}
	if answers := binary.BigEndian.Uint16(wire[6:8]); answers == 0 {
		return status, fmt.Errorf("reply carries no answer record")
	}

	// Skip the question name, if the node echoed one, then the answer's own
	// name, type, class and TTL, to reach the record's data.
	pos := 12
	if qd := binary.BigEndian.Uint16(wire[4:6]); qd > 0 {
		var err error
		if pos, err = skipName(wire, pos); err != nil {
			return status, err
		}
		pos += 4 // question type and class
	}
	pos, err := skipName(wire, pos)
	if err != nil {
		return status, err
	}
	pos += 2 + 2 + 4 // type, class, TTL
	if pos+2 > len(wire) {
		return status, fmt.Errorf("reply ends before its answer record")
	}
	length := int(binary.BigEndian.Uint16(wire[pos : pos+2]))
	pos += 2
	if pos+length > len(wire) {
		return status, fmt.Errorf("answer says %d bytes of data but only %d remain", length, len(wire)-pos)
	}

	data := wire[pos : pos+length]
	if len(data) < 1 {
		return status, fmt.Errorf("answer carries no name count")
	}
	count := int(data[0])
	data = data[1:]
	if len(data) < count*(nameLen+1+2) {
		return status, fmt.Errorf("answer promises %d names but is too short to hold them", count)
	}
	for i := 0; i < count; i++ {
		entry := data[i*18 : i*18+18]
		flags := binary.BigEndian.Uint16(entry[16:18])
		status.Names = append(status.Names, Name{
			Name:   strings.TrimRight(string(entry[:nameLen]), " \x00"),
			Suffix: entry[nameLen],
			Group:  flags&groupFlag != 0,
			Flags:  flags,
		})
	}

	// The adapter's own address follows the names, ahead of statistics shoal
	// has no use for. Samba fills it with zeroes.
	if rest := data[count*18:]; len(rest) >= 6 {
		mac := net.HardwareAddr(append([]byte(nil), rest[:6]...))
		if !allZero(mac) {
			status.MAC = mac
		}
	}
	return status, nil
}

// skipName steps over a name, which is either a length-prefixed label chain
// or a compression pointer.
func skipName(wire []byte, pos int) (int, error) {
	for {
		if pos >= len(wire) {
			return 0, fmt.Errorf("name runs past the end of the reply")
		}
		length := int(wire[pos])
		switch {
		case length == 0:
			return pos + 1, nil
		case length&0xc0 == 0xc0:
			return pos + 2, nil // a pointer is always the last thing in a name
		default:
			pos += length + 1
		}
	}
}

// Workstation returns the name the node holds for itself: unique, and
// registered for the workstation service.
func (s NodeStatus) Workstation() (Name, bool) {
	for _, n := range s.Names {
		if !n.Group && n.Suffix == 0x00 {
			return n, true
		}
	}
	return Name{}, false
}

// Workgroup returns the domain or workgroup the node belongs to.
func (s NodeStatus) Workgroup() (Name, bool) {
	for _, n := range s.Names {
		if n.Group && n.Suffix == 0x00 {
			return n, true
		}
	}
	return Name{}, false
}

// Summary describes a name table in one line for the event log.
func (s NodeStatus) Summary() string {
	parts := make([]string, 0, len(s.Names)+1)
	for _, n := range s.Names {
		parts = append(parts, n.String())
	}
	if s.MAC != nil {
		parts = append(parts, "adapter "+s.MAC.String())
	}
	return strings.Join(parts, "; ")
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
