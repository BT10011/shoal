//go:build windows

package icmp

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestTheEchoAPIPingsLoopbackWithoutPrivileges(t *testing.T) {
	conn, mode, err := Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if mode != ModeEchoAPI {
		t.Fatalf("mode = %q, want %q", mode, ModeEchoAPI)
	}
	req, _ := Echo(1, 1)
	conn.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := conn.WriteTo(req, mode.addr(net.IPv4(127, 0, 0, 1))); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, from, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no answer from 127.0.0.1 through IcmpSendEcho: %v", err)
	}
	reply, err := ParseReply(buf[:n])
	if err != nil || !reply.Echo || !reply.Mine || reply.Seq != 1 {
		t.Fatalf("reply = %+v (%v), want an echo reply to seq 1 carrying shoal's payload", reply, err)
	}
	if host(from) != "127.0.0.1" {
		t.Errorf("from = %v, want 127.0.0.1", from)
	}
}
