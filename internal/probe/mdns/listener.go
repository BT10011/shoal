package mdns

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"

	"github.com/BT10011/shoal/internal/dnswire"
	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
)

// MetaQuery is the name that lists the service types on a network. A
// responder answering it says "here are the kinds of thing I offer".
const MetaQuery = "_services._dns-sd._udp.local"

// readSlice is how long a read waits before the listener checks whether it
// has been cancelled. It bounds shutdown time, nothing else.
const readSlice = 500 * time.Millisecond

// ListenerOptions tune the passive listener.
type ListenerOptions struct {
	Group  *net.UDPAddr
	Listen func(ctx context.Context, iface netif.Interface, group *net.UDPAddr) (net.PacketConn, error)
	// Browse lists service types to ask for once per scan; nil sends nothing
	// and only listens. DefaultBrowse is what the TUI asks.
	Browse []string
	// Send puts a question on the wire: SendFrom5353.
	Send func(ctx context.Context, iface netif.Interface, group *net.UDPAddr, query []byte) error
	// Settle overrides how long a listed service type waits to be credited;
	// zero means DefaultSettle. Tests set it to skip the wait.
	Settle time.Duration
	// AskLocal asks this machine's own responder what it offers; nil means
	// AskLocalResponder. See askLocal for why it has to be asked at all.
	AskLocal func(ctx context.Context, questions []string, wait time.Duration) ([][]byte, error)
	// LocalWait bounds how long that answer is waited for; zero means
	// DefaultLocalWait.
	LocalWait time.Duration
}

// DefaultBrowse is the question shoal asks the network at the start of each
// scan: every service type on offer (RFC 6763 §9), and Dante's and NDI's
// own types, since not every embedded responder answers the enumeration.
// Everything that answers is credited by the same rules as an announcement.
//
// Two Dante types are asked for, not one. `_netaudio-arc._udp` is audio
// routing control, and `_netaudio-cmc._udp` is the control and monitoring
// channel that every Dante device runs — a device answering only the
// latter would be missed by a browse that asked only for routing.
var DefaultBrowse = []string{
	MetaQuery,
	"_netaudio-cmc._udp.local",
	"_netaudio-arc._udp.local",
	"_ndi._tcp.local",
}

// browseAgain is when the question is repeated: RFC 6762 §5.2 asks that
// the first two queries of a browse be at least a second apart, since a
// responder may miss the first.
const browseAgain = time.Second

// LocalResponder is where this machine's own mDNS responder is asked
// directly. See askLocal.
const LocalResponder = "127.0.0.1:5353"

// DefaultLocalWait bounds how long shoal waits for that answer. It comes
// back off loopback, so this is generous.
const DefaultLocalWait = 1500 * time.Millisecond

// DefaultSettle is how long a listed service type waits before it is credited to
// the sender that listed it. A device that names its own address is
// credited at once; this is the pause for everything else, and it is there
// to give a relay time to give itself away, since a relay is recognised by
// the foreign addresses it carries rather than by any one message.
const DefaultSettle = 2 * time.Second

// AskLocalResponder asks this machine's own mDNS responder, by unicast, for
// PTR records of each name, and returns the messages it sends back.
//
// This exists because a responder does not answer multicast queries that
// came from its own host: mDNSResponder and Avahi both ignore their own
// machine's questions, so shoal listening on the group learns the services
// of every device except the one it is running on. That was found with NDI
// Scan Converter advertising happily on a laptop while shoal, on the same
// laptop, never saw it. A unicast question to 127.0.0.1:5353 is answered at
// once, so this is the one device shoal asks directly instead of
// overhearing. Nothing extra reaches the network: it goes over loopback.
//
// The query carries a random ID and answers that do not echo it are
// dropped, the same rule the rdns probe follows.
func AskLocalResponder(ctx context.Context, questions []string, wait time.Duration) ([][]byte, error) {
	id := randomID()
	query, err := browseQuery(id, questions...)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp4", LocalResponder)
	if err != nil {
		return nil, fmt.Errorf("cannot reach this machine's own responder on %s: %w", LocalResponder, err)
	}
	defer conn.Close()
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	var out [][]byte
	buf := make([]byte, 9000)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return out, nil // the deadline: however much it said by then
		}
		wire := append([]byte(nil), buf[:n]...)
		if resp, err := ParseResponse(wire); err == nil && resp.ID == id {
			out = append(out, wire)
		}
	}
}

// BrowseQuery builds one mDNS message asking for PTR records of each name,
// ID 0 as multicast questions carry.
func BrowseQuery(names ...string) ([]byte, error) { return browseQuery(0, names...) }

func browseQuery(id uint16, names ...string) ([]byte, error) {
	msg := dnsmessage.Message{Header: dnsmessage.Header{ID: id}}
	for _, n := range names {
		name, err := dnsmessage.NewName(strings.TrimSuffix(n, ".") + ".")
		if err != nil {
			return nil, fmt.Errorf("%q is not a usable DNS name: %w", n, err)
		}
		msg.Questions = append(msg.Questions, dnsmessage.Question{Name: name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET})
	}
	return msg.Pack()
}

func (o ListenerOptions) withDefaults() ListenerOptions {
	if o.Group == nil {
		o.Group = &net.UDPAddr{IP: net.ParseIP(Group), Port: Port}
	}
	if o.Listen == nil {
		o.Listen = ListenMulticast
	}
	if o.Send == nil {
		o.Send = SendFrom5353
	}
	if o.Settle == 0 {
		o.Settle = DefaultSettle
	}
	if o.AskLocal == nil {
		o.AskLocal = AskLocalResponder
	}
	if o.LocalWait == 0 {
		o.LocalWait = DefaultLocalWait
	}
	return o
}

// Listener is a discoverer that says nothing and writes down what it hears.
// Bonjour networks are noisy: devices announce themselves when they join,
// when they wake, and whenever a service changes, so simply listening finds
// devices and their services without sending a single packet.
type Listener struct {
	opts ListenerOptions

	// own records the names each sender has claimed for itself, so that a
	// service can be credited to the device that actually offers it. Bonjour
	// devices relay each other's records — a phone will announce a PTR for a
	// laptop's instance — and crediting the sender would be wrong.
	own map[string]map[string]bool

	// held keeps the service types a sender has listed until they are
	// credited: at once if the sender names its own address, otherwise
	// after settle, and never if the sender is found to be a relay.
	held map[string]map[string]heldService

	// srv keeps the services a sender has announced but not yet tied to
	// itself, because the SRV naming the serving host arrived before the
	// address record that proves the host is the sender. mDNS responders
	// split long answers across messages, and either order is legal, so a
	// service announced the wrong way round used to be lost.
	srv map[string][]heldSRV

	// relay records the senders found to be speaking for somebody else,
	// against the reason they gave themselves away. A router running an
	// mDNS repeater or reflector answers "which services are here?" for
	// devices on other networks, and crediting it with their services
	// would put a whole VLAN's worth of equipment on the router.
	relay map[string]string

	// asked holds the service types the browse names, so an answer to a
	// question shoal put itself can be told from an announcement nobody
	// asked for. See service.
	asked map[string]bool

	// subnet is the scanning interface's network, which is what makes an
	// address in a relayed record recognisably foreign.
	subnet *net.IPNet
}

type heldService struct {
	rec  Record
	wire []byte
	at   time.Time // when it was heard, so settle can be measured
	// evidence is the sentence saying what the sender did to earn the
	// service: listed it among its types, or answered a browse for it.
	evidence string
}

// heldSRV is a service waiting for its serving host to be tied to the sender.
type heldSRV struct {
	service string
	rec     Record
	wire    []byte
}

// NewListener creates the probe.
func NewListener(opts ListenerOptions) *Listener {
	l := &Listener{opts: opts.withDefaults(), own: make(map[string]map[string]bool),
		held: make(map[string]map[string]heldService), relay: make(map[string]string),
		srv: make(map[string][]heldSRV), asked: make(map[string]bool)}
	for _, name := range l.opts.Browse {
		if service, ok := ServiceType(name); ok {
			l.asked[service] = true
		}
	}
	return l
}

// speaksForItself reports whether a sender has named its own address, by
// an address record or by answering for its own reverse name.
func (l *Listener) speaksForItself(key string) bool { return len(l.own[key]) > 0 }

// claims records that key announced name as its own.
func (l *Listener) claims(key, name string) {
	names := l.own[key]
	if names == nil {
		names = make(map[string]bool)
		l.own[key] = names
	}
	names[strings.ToLower(trimDot(name))] = true
}

// owns reports whether key has announced an address record for name.
func (l *Listener) owns(key, name string) bool {
	return l.own[key][strings.ToLower(trimDot(name))]
}

// ListenerName is how the listener appears in the hood pane and the log,
// kept apart from the mdns enricher that asks questions: one hears the
// announcements devices make (DNS Service Discovery, RFC 6763, the traffic
// behind Bonjour and Avahi), the other asks each device its name.
const ListenerName = "dns-sd"

// Source is what the listener's facts are credited to. They are mDNS
// records like the enricher's answers, so they share its name and with it
// its priority, its weight as direct contact and its renewal.
const Source = "mdns"

func (l *Listener) Name() string { return ListenerName }

// ListenMulticast joins the mDNS group on an interface. With no interface
// named, the system's default for multicast is used.
//
// Port 5353 is already held by the system responder — mDNSResponder on macOS,
// avahi-daemon on most Linux systems — so the socket asks for SO_REUSEADDR
// and SO_REUSEPORT before binding. Those are what let several processes share
// a multicast port, and they are why shoal can watch the conversation without
// disturbing the responder that is having it. Binding the group address,
// rather than any address, is the other half of not disturbing it: only
// multicast arrives here, and unicast traffic for the port stays with the
// responder it was meant for. bindGroup says why that needs doing by hand.
func ListenMulticast(ctx context.Context, iface netif.Interface, group *net.UDPAddr) (net.PacketConn, error) {
	conn, err := bindGroup(group)
	if err != nil {
		// Better a listener that overhears too much than none at all.
		lc := net.ListenConfig{Control: shareablePort}
		conn, err = lc.ListenPacket(ctx, "udp4", fmt.Sprintf("%s:%d", group.IP, group.Port))
		if err != nil {
			return nil, fmt.Errorf("cannot bind %s: %w", group, err)
		}
	}
	ni, err := interfaceFor(iface)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := ipv4.NewPacketConn(conn).JoinGroup(ni, group); err != nil {
		conn.Close()
		where := "the default interface"
		if ni != nil {
			where = iface.Name
		}
		return nil, fmt.Errorf("cannot join %s on %s: %w", group.IP, where, err)
	}
	return conn, nil
}

// shareablePort sets the options that let a socket share port 5353 with the
// system's own responder. BSD and macOS need SO_REUSEPORT on every socket
// bound to the port; Linux accepts SO_REUSEADDR on both, whoever owns the
// other one.
func shareablePort(_, _ string, c syscall.RawConn) error {
	var opErr error
	err := c.Control(func(fd uintptr) {
		if opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); opErr != nil {
			return
		}
		opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	})
	if err != nil {
		return err
	}
	return opErr
}

// interfaceFor resolves the scanning interface, or nil to let the system
// choose when none is named.
func interfaceFor(iface netif.Interface) (*net.Interface, error) {
	if iface.Name == "" {
		return nil, nil
	}
	ni, err := net.InterfaceByName(iface.Name)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", iface.Name, err)
	}
	return ni, nil
}

// Run listens until the context is cancelled.
func (l *Listener) Run(ctx context.Context, iface netif.Interface, emit engine.Emit, report engine.Report) error {
	conn, err := l.opts.Listen(ctx, iface, l.opts.Group)
	if err != nil {
		return err
	}
	defer conn.Close()
	l.subnet = iface.Subnet

	var query []byte
	if len(l.opts.Browse) > 0 {
		if query, err = BrowseQuery(l.opts.Browse...); err != nil {
			return err
		}
		report(engine.ProbeEvent{Kind: engine.KindInfo,
			Message: fmt.Sprintf("listening on %s via %s, sharing the port with the system responder; asking once which services are on offer, including Dante and NDI, then only listening", l.opts.Group, iface.Name)})
	} else {
		report(engine.ProbeEvent{Kind: engine.KindInfo,
			Message: fmt.Sprintf("listening on %s via %s, sharing the port with the system responder; nothing is sent", l.opts.Group, iface.Name)})
	}
	// This machine's own responder, asked directly because it will not
	// answer its own multicast. The answers come back on a channel so that
	// only this goroutine ever touches the listener's state.
	local := l.startLocalAsk(ctx, iface, report)
	localFrom := &net.UDPAddr{IP: iface.IP, Port: Port}

	asked := 0
	nextAsk := time.Now()

	heard := 0
	buf := make([]byte, 9000)
	for {
		if err := ctx.Err(); err != nil {
			l.creditSettled(time.Time{}, emit, report) // whatever is left and not a relay
			l.reportUncredited(report)
			report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("stopped listening after %d messages", heard)})
			return nil
		}
		if query != nil && asked < 2 && !time.Now().Before(nextAsk) {
			asked++
			nextAsk = time.Now().Add(browseAgain)
			if err := l.opts.Send(ctx, iface, l.opts.Group, query); err != nil {
				query = nil
				report(engine.ProbeEvent{Kind: engine.KindInfo, Target: l.opts.Group.String(),
					Message: fmt.Sprintf("could not ask which services are on offer (%v); listening only, so services appear when something else browses", err)})
			} else {
				report(engine.ProbeEvent{Kind: engine.KindSent, Target: l.opts.Group.String(),
					Message: fmt.Sprintf("PTR? %s → %s (%d bytes, browse %d of 2, from port %d: every responder offering these answers by multicast)", strings.Join(l.opts.Browse, ", "), l.opts.Group, len(query), asked, Port)})
			}
		}
		if err := conn.SetReadDeadline(time.Now().Add(readSlice)); err != nil {
			return err
		}
		l.creditSettled(time.Now(), emit, report)
		select {
		case wire, ok := <-local:
			if !ok {
				local = nil // drained; a nil channel never fires again
				break
			}
			heard++
			l.overheard(wire, localFrom, true, emit, report)
		default:
		}
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			continue // a deadline, so the loop can check for cancellation
		}
		heard++
		report(engine.ProbeEvent{Kind: engine.KindProgress, Done: heard, Message: "listening"})
		l.overheard(append([]byte(nil), buf[:n]...), from, false, emit, report)
	}
}

// startLocalAsk asks this machine's own responder in the background and
// hands the answers back on a channel. Nothing but reporting happens in the
// goroutine, so the listener's own bookkeeping stays single-threaded.
func (l *Listener) startLocalAsk(ctx context.Context, iface netif.Interface, report engine.Report) <-chan []byte {
	out := make(chan []byte, 16)
	if len(l.opts.Browse) == 0 || iface.IP == nil {
		close(out)
		return out
	}
	go func() {
		defer close(out)
		report(engine.ProbeEvent{Kind: engine.KindSent, Target: LocalResponder,
			Message: fmt.Sprintf("PTR? %s → %s (unicast to this machine's own responder, which ignores multicast questions from its own host, so its services would otherwise never be seen)",
				strings.Join(l.opts.Browse, ", "), LocalResponder)})
		msgs, err := l.opts.AskLocal(ctx, l.opts.Browse, l.opts.LocalWait)
		if err != nil {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Target: LocalResponder,
				Message: fmt.Sprintf("could not ask this machine's own responder (%v); the services of the machine shoal is running on will be missing unless something else asks for them", err)})
			return
		}
		if len(msgs) == 0 {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Target: LocalResponder,
				Message: "this machine's own responder said nothing; either nothing is registered here or no responder is running"})
			return
		}
		for _, m := range msgs {
			select {
			case out <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// overheard turns one message into facts about whoever sent it. Everything is
// attributed to the sender's address: a responder speaks only for itself, and
// the store links that address to a MAC once ARP has found one.
func (l *Listener) overheard(wire []byte, from net.Addr, asked bool, emit engine.Emit, report engine.Report) {
	addr, ok := from.(*net.UDPAddr)
	if !ok {
		return
	}
	// The system responder announces on the loopback interface as well as
	// on the network, and a socket on the group hears both. Those datagrams
	// are this machine talking to itself: written down, they become a
	// device called 127.0.0.1 that no scan can account for, and that every
	// other probe then goes and asks questions of. This machine is asked
	// directly instead (see askLocal), and credited to the address it
	// scans from.
	if !asked && addr.IP.IsLoopback() {
		return
	}
	key := addr.IP.String()
	how := fmt.Sprintf("sent multicast DNS from this address to %s, overheard without asking", l.opts.Group)
	heardIt := "announced"
	if asked {
		how = fmt.Sprintf("answered a direct question to this machine's own responder on %s; a responder ignores multicast questions from its own host, so this one device is asked rather than overheard", LocalResponder)
		heardIt = "answered"
	}

	resp, err := ParseResponse(wire)
	if err != nil {
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("%d bytes from %s could not be decoded: %v", len(wire), key, err)})
		return
	}
	if len(resp.Records) == 0 {
		// A query, not an announcement. It still proves somebody is there.
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: key,
			Message: fmt.Sprintf("%s asked about %s", key, resp.Question)})
	} else {
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: key,
			Message: fmt.Sprintf("%s %s: %s", key, heardIt, resp.Summary())})
	}

	emit(model.Observation{
		Source:    Source,
		DeviceKey: key, Field: model.FieldIP, Value: key, Confidence: 0.9,
		Method: how,
	})

	// Addresses first: what a device says about its own name decides which
	// services can be credited to it below, and which of the addresses it
	// carries are somebody else's. Answering for its own reverse name,
	// which shoal's mdns probe asks every device, counts as well.
	ownReverse, _ := dnswire.ReverseName(addr.IP)
	for _, rec := range resp.Records {
		if rec.Type == dnsmessage.TypePTR && ownReverse != "" && trimDot(rec.Name) == trimDot(ownReverse) {
			l.claims(key, rec.PTR)
		}
	}
	for _, rec := range resp.Records {
		if rec.Type != dnsmessage.TypeA || !rec.Addr.Equal(addr.IP) {
			continue
		}
		l.claims(key, rec.Name)
		emit(model.Observation{
			Source:    Source,
			DeviceKey: key, Field: model.FieldHostname, Value: rec.Name,
			Confidence: Confidence, Raw: wire, TTL: max(rec.TTL, minTTL),
			Method: fmt.Sprintf("mDNS A record %s = %s, announced by the device itself (%s section, mDNS TTL %s)",
				rec.Name, rec.Addr, rec.Section, rec.TTL),
		})
	}

	// Now that the sender's own names are known: is it speaking for
	// somebody else? This runs before anything is credited.
	l.checkRelay(key, addr.IP, resp, report)

	l.releaseSRV(key, emit, report)

	if l.speaksForItself(key) {
		l.release(key, emit, report)
	}

	for _, rec := range resp.Records {
		switch rec.Type {
		case dnsmessage.TypePTR:
			l.service(key, rec, wire, emit, report)
		case dnsmessage.TypeSRV:
			service, ok := ServiceType(rec.Name)
			if !ok {
				continue
			}
			// The SRV names the host that actually serves the instance. Only
			// when that host is one the sender has claimed is the service
			// the sender's own.
			if !l.owns(key, rec.SRV.Target) {
				l.holdSRV(key, service, rec, wire, report)
				continue
			}
			emit(l.srvObservation(key, service, rec, wire, "announced %s as its own name"))
		}
	}
}

// service emits what a PTR record proves about the sender.
//
// Two shapes speak in the sender's own voice. An answer to the meta-query
// says "these are the types I offer". An answer to a browse shoal itself
// sent — `_ndi._tcp.local → STAGE-MBP.LOCAL (Scan Converter)._ndi._tcp.local`
// — says "I have one of those", because a responder answers a question only
// for its own records. Both are credited by the same rules below.
//
// The same shape for a type nobody asked about proves nothing: devices
// really do announce each other's instances, so that is logged and left to
// the SRV record, which names the serving host and can be checked.
//
// Crediting an answer to our own browse is what makes the Dante and NDI
// types in DefaultBrowse worth asking for. Plenty of responders — embedded
// AV gear especially — answer a browse with the PTR alone and leave out the
// SRV and address records that a Mac or an avahi host throws in, and
// waiting for those meant the answer to the question shoal had just asked
// was thrown away.
func (l *Listener) service(key string, rec Record, wire []byte, emit engine.Emit, report engine.Report) {
	name, evidence, ok := l.inItsOwnVoice(rec)
	if !ok {
		if service, ok := ServiceType(rec.Name); ok {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
				Message: fmt.Sprintf("%s announced the instance %s of %s, a type nobody asked about; waiting for an SRV record to say which host serves it", key, rec.PTR, service)})
		}
		return
	}
	if reason, relaying := l.relay[key]; relaying {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("%s offers %s, but it is relaying another network's mDNS (%s), so the service is not its own", key, name, reason)})
		return
	}
	if !l.speaksForItself(key) {
		l.hold(key, name, evidence, rec, wire, report)
		return
	}
	emit(model.Observation{
		Source:    Source,
		DeviceKey: key, Field: model.FieldService, Value: name,
		Confidence: Confidence, Raw: wire, TTL: max(rec.TTL, minTTL),
		Method: fmt.Sprintf("%s (mDNS TTL %s)", evidence, rec.TTL),
	})
}

// inItsOwnVoice reports whether a PTR record is the sender speaking about
// what it offers, and if so which service type and by what evidence.
func (l *Listener) inItsOwnVoice(rec Record) (name, evidence string, ok bool) {
	if rec.Name == MetaQuery {
		name = trimSuffix(rec.PTR)
		if name == "" {
			return "", "", false
		}
		return name, fmt.Sprintf("mDNS PTR under %s: the device lists %s among the service types it offers", MetaQuery, name), true
	}
	service, ok := ServiceType(rec.Name)
	// The record must be owned by the service type itself, which is what an
	// answer to a browse looks like; a PTR owned by an instance is somebody
	// describing one particular instance, not a device answering for a type.
	if !ok || !l.asked[service] || trimSuffix(rec.Name) != service {
		return "", "", false
	}
	return service, fmt.Sprintf("mDNS PTR under %s: the device answered shoal's browse for %s with %s, an instance of its own",
		rec.Name, service, rec.PTR), true
}

// hold keeps a service type until its sender names its own address.
func (l *Listener) hold(key, name, evidence string, rec Record, wire []byte, report engine.Report) {
	if l.held[key] == nil {
		l.held[key] = make(map[string]heldService)
	}
	if _, already := l.held[key][name]; already {
		return
	}
	l.held[key][name] = heldService{rec: rec, wire: wire, at: time.Now(), evidence: evidence}
	report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
		Message: fmt.Sprintf("%s offers %s but has not named its own address; holding it %s in case this sender turns out to be relaying another network's mDNS", key, name, l.opts.Settle)})
}

// checkRelay looks for the one thing that gives a relay away: an address
// record for somebody else's address, on a network that is not this one. A
// device speaks for itself and carries only its own address; a repeater or
// reflector carries whole other subnets. Only A records are read, because
// an IPv6 address cannot be compared against the IPv4 address the message
// arrived from.
//
// A name the sender has already claimed is not somebody else's, whatever
// network the address is on. Devices are multi-homed: a Dante interface has
// a redundant second port on its own network, laptops carry VPN addresses,
// and a responder that lists every address its host answers to was being
// read as a repeater and losing every service it offered.
func (l *Listener) checkRelay(key string, src net.IP, resp Response, report engine.Report) {
	if _, already := l.relay[key]; already {
		return
	}
	for _, rec := range resp.Records {
		if rec.Type != dnsmessage.TypeA || !hostRecord(rec.Name) || l.owns(key, rec.Name) {
			continue
		}
		if !l.foreign(rec.Addr, src) {
			continue
		}
		reason := fmt.Sprintf("it carried %s %s = %s, an address on another network", rec.Section, rec.Name, rec.Addr)
		l.relay[key] = reason
		held := l.held[key]
		delete(l.held, key)
		delete(l.srv, key)
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("%s is relaying another network's mDNS: %s. Nothing it lists is credited to it, since a repeater or reflector answers for devices it only forwards for; %d service types it had listed are dropped", key, reason, len(held))})
		return
	}
}

// srvObservation builds the service fact an SRV record proves, with why the
// serving host counts as the sender's in the method.
func (l *Listener) srvObservation(key, service string, rec Record, wire []byte, why string) model.Observation {
	return model.Observation{
		Source:    Source,
		DeviceKey: key, Field: model.FieldService, Value: service,
		Confidence: Confidence, Raw: wire, TTL: max(rec.TTL, minTTL),
		Method: fmt.Sprintf("mDNS SRV record %s = %s:%d, and %s "+why+" (mDNS TTL %s)",
			rec.Name, rec.SRV.Target, rec.SRV.Port, key, rec.SRV.Target, rec.TTL),
	}
}

// holdSRV keeps a service whose serving host the sender has not claimed yet.
// The address record may simply be in the next message: responders split
// long answers, and nothing says the address must come first.
func (l *Listener) holdSRV(key, service string, rec Record, wire []byte, report engine.Report) {
	if _, relaying := l.relay[key]; relaying {
		return
	}
	for _, h := range l.srv[key] {
		if h.service == service && h.rec.SRV.Target == rec.SRV.Target {
			return
		}
	}
	l.srv[key] = append(l.srv[key], heldSRV{service: service, rec: rec, wire: wire})
	report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
		Message: fmt.Sprintf("%s announced %s served by %s, a name it has not claimed as its own; holding the service until it announces an address for that name",
			key, rec.Name, rec.SRV.Target)})
}

// releaseSRV credits the held services whose serving host the sender has
// since claimed as its own.
func (l *Listener) releaseSRV(key string, emit engine.Emit, report engine.Report) {
	held := l.srv[key]
	if len(held) == 0 {
		return
	}
	kept := held[:0:0]
	for _, h := range held {
		if !l.owns(key, h.rec.SRV.Target) {
			kept = append(kept, h)
			continue
		}
		emit(l.srvObservation(key, h.service, h.rec, h.wire, "has since announced %s as its own name"))
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("%s has now claimed %s, so the %s it announced earlier is its own", key, h.rec.SRV.Target, h.service)})
	}
	if len(kept) == 0 {
		delete(l.srv, key)
		return
	}
	l.srv[key] = kept
}

// hostRecord reports whether a name is the kind mDNS gives a host: a single
// label under .local (RFC 6762 §3). Only an address record for one of those
// is another machine's, and so evidence that the sender speaks for it.
//
// Plenty of address records are owned by something that is not a host, and
// reading them as one was calling working AV gear a repeater. Dante
// registers a record per multicast flow, named for the reversed flow
// address, whose address is the transmitter — on the Dante network, which is
// not the one being scanned. A device announcing where its audio comes from
// is not forwarding another network's mDNS, and it was losing every service
// it offered for saying so.
//
// A host whose own name contains a dot is read as several labels and so is
// not counted; that direction is the safe one, since it means a relay goes
// unnoticed rather than a device losing what it offers.
func hostRecord(name string) bool {
	rest, ok := strings.CutSuffix(strings.ToLower(trimDot(name)), ".local")
	if !ok || rest == "" {
		return false
	}
	return !strings.Contains(rest, ".")
}

// foreign reports whether ip belongs to something other than the sender and
// to a network other than the one being scanned.
// With no subnet known there is nothing to judge an address against, so
// nothing is called foreign: the cost of guessing wrong here is a device
// losing every service it offers, which is worse than a relay going
// unnoticed in a run that could not even name the interface.
func (l *Listener) foreign(ip, src net.IP) bool {
	if ip == nil || ip.Equal(src) || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return false
	}
	return l.subnet != nil && !l.subnet.Contains(ip)
}

// release credits what a sender listed before it named its own address.
func (l *Listener) release(key string, emit engine.Emit, report engine.Report) {
	names := l.creditHeld(key, time.Time{}, "credited once it had named its own address", emit)
	if len(names) == 0 {
		return
	}
	what := fmt.Sprintf("the %d service types it listed earlier are its own", len(names))
	if len(names) == 1 {
		what = "the service type it listed earlier is its own"
	}
	report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
		Message: fmt.Sprintf("%s has named its own address, so %s: %s", key, what, strings.Join(names, ", "))})
}

// creditSettled credits the lists that have waited out settle without their
// sender turning out to be a relay. A zero now credits everything left,
// which is what happens when listening stops.
func (l *Listener) creditSettled(now time.Time, emit engine.Emit, report engine.Report) {
	keys := make([]string, 0, len(l.held))
	for key := range l.held {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		names := l.creditHeld(key, now, fmt.Sprintf("credited after %s with no sign that this sender was relaying another network", l.opts.Settle), emit)
		if len(names) == 0 {
			continue
		}
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("%s listed %s and did not carry any other network's addresses, so the list is its own", key, strings.Join(names, ", "))})
	}
}

// creditHeld emits the service types key is holding, oldest first by name.
// Entries younger than settle are kept unless now is zero. A relay is never
// credited. It returns the names it emitted.
func (l *Listener) creditHeld(key string, now time.Time, why string, emit engine.Emit) []string {
	if _, relaying := l.relay[key]; relaying {
		return nil
	}
	held := l.held[key]
	if len(held) == 0 {
		return nil
	}
	names := make([]string, 0, len(held))
	for name, h := range held {
		if now.IsZero() || now.Sub(h.at) >= l.opts.Settle {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		h := held[name]
		delete(held, name)
		emit(model.Observation{
			DeviceKey: key, Field: model.FieldService, Value: name, Source: Source,
			Confidence: Confidence, Raw: h.wire, TTL: max(h.rec.TTL, minTTL),
			Method: fmt.Sprintf("%s, %s (mDNS TTL %s)", h.evidence, why, h.rec.TTL),
		})
	}
	if len(held) == 0 {
		delete(l.held, key)
	}
	return names
}

// reportUncredited says, when listening stops, which senders were found to
// be relaying, so a missing service is explained rather than absent.
func (l *Listener) reportUncredited(report engine.Report) {
	keys := make([]string, 0, len(l.relay))
	for key := range l.relay {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("not credited: %s was relaying another network's mDNS (%s), so the service types it listed belong to the devices behind it, not to it", key, l.relay[key])})
	}
}

// ServiceType pulls the service type out of a DNS-SD name: "_airplay._tcp"
// from "Living Room._airplay._tcp.local". The type is the pair of labels
// where the second is the transport.
func ServiceType(name string) (string, bool) {
	if trimSuffix(name) == trimSuffix(MetaQuery) {
		return "", false
	}
	labels := strings.Split(trimDot(name), ".")
	for i := 0; i+1 < len(labels); i++ {
		if !strings.HasPrefix(labels[i], "_") {
			continue
		}
		if labels[i+1] == "_tcp" || labels[i+1] == "_udp" {
			return labels[i] + "." + labels[i+1], true
		}
	}
	return "", false
}

// trimSuffix drops the ".local" a DNS-SD name always ends with, which is the
// same on every row and so carries no information on screen.
func trimSuffix(name string) string {
	return strings.TrimSuffix(trimDot(name), ".local")
}
