package ui

import (
	"errors"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/BT10011/shoal/internal/netif"
)

// fakeCapture stands in for capture.Tap.
type fakeCapture struct {
	mu       sync.Mutex
	on       bool
	packets  int
	startErr error
	starts   []string // interface names recording was started on
}

func (f *fakeCapture) Start(iface netif.Interface) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.on = true
	f.starts = append(f.starts, iface.Name)
	return "/captures/shoal-20261011-093000.pcapng", nil
}

func (f *fakeCapture) Stop() (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.on = false
	return "/captures/shoal-20261011-093000.pcapng", f.packets, nil
}

func (f *fakeCapture) Recording() (string, int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "/captures/shoal-20261011-093000.pcapng", f.packets, f.on
}

func press(a *app, key string) {
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	run(a, cmd)
}

func lastLog(a *app) string { return a.events[len(a.events)-1].Message }

func TestWRecordsUntilPressedAgain(t *testing.T) {
	eth0 := candidate(t, "eth0", "172.16.10.5", "172.16.10.0/24")
	a, _ := switchingApp(t, eth0)
	c := &fakeCapture{}
	a.opts.Capture = c
	run(a, a.switchTo(eth0, SweepAsk))
	a.Update(tea.WindowSizeMsg{Width: 200, Height: 40}) // room for every key, w being the first to go

	if view := ansi.Strip(a.View()); !strings.Contains(view, "w pcap") || strings.Contains(view, "REC") {
		t.Fatalf("before recording the key bar offers w and nothing says REC:\n%s", view)
	}
	press(a, "w")
	if !c.on || len(c.starts) != 1 || c.starts[0] != "eth0" {
		t.Fatalf("w should start recording on the interface being scanned: %+v", c)
	}
	if msg := lastLog(a); !strings.Contains(msg, "/captures/shoal-20261011-093000.pcapng") || !strings.Contains(msg, "press r to rescan") {
		t.Errorf("the log should say where, and how to capture a whole scan: %q", msg)
	}
	c.mu.Lock()
	c.packets = 42
	c.mu.Unlock()
	view := ansi.Strip(a.View())
	if !strings.Contains(view, "REC 42") || !strings.Contains(view, "w stop rec") {
		t.Errorf("while recording the status bar counts packets and the key bar says how to stop:\n%s", view)
	}

	press(a, "w")
	if c.on {
		t.Fatal("w again should stop")
	}
	if msg := lastLog(a); !strings.Contains(msg, "saved 42 packets to /captures/shoal-20261011-093000.pcapng") || !strings.Contains(msg, "Wireshark") {
		t.Errorf("stopping should say what was saved and where to open it: %q", msg)
	}
}

func TestRecordingEndsWhenSwitchingInterface(t *testing.T) {
	eth0 := candidate(t, "eth0", "172.16.10.5", "172.16.10.0/24")
	eth1 := candidate(t, "eth1", "198.51.100.7", "198.51.100.0/24")
	a, _ := switchingApp(t, eth0, eth1)
	c := &fakeCapture{}
	a.opts.Capture = c
	run(a, a.switchTo(eth0, SweepAsk))
	press(a, "w")

	run(a, a.switchTo(eth1, SweepAsk))
	if c.on {
		t.Fatal("one file is one network: switching must end the recording")
	}
	var said bool
	for _, ev := range a.events {
		if strings.Contains(ev.Message, "stopped recording (switched to eth1)") {
			said = true
		}
	}
	if !said {
		t.Error("the log should say the recording ended, and why")
	}
}

func TestRecordingThatCannotStartSaysWhy(t *testing.T) {
	eth0 := candidate(t, "eth0", "172.16.10.5", "172.16.10.0/24")
	a, _ := switchingApp(t, eth0)
	a.opts.Capture = &fakeCapture{startErr: errors.New("permission denied")}
	run(a, a.switchTo(eth0, SweepAsk))
	press(a, "w")
	if msg := lastLog(a); !strings.Contains(msg, "could not start recording: permission denied") {
		t.Errorf("log = %q", msg)
	}
	if strings.Contains(ansi.Strip(a.View()), "REC") {
		t.Error("nothing is recording")
	}
}

func TestTheDemoHasNoCapture(t *testing.T) {
	a, _ := sized(t, 150, 40)
	press(a, "w")
	if strings.Contains(ansi.Strip(a.View()), "pcap") {
		t.Error("the demo sends no packets, so it offers no capture")
	}
}
