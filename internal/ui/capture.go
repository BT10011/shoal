package ui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
)

// Capture records the packets behind the probe events to a pcapng file
// for Wireshark. The UI only starts and stops it, off its own goroutine,
// since both touch a file. Implemented by capture.Tap.
type Capture interface {
	Start(iface netif.Interface) (path string, err error)
	Stop() (path string, packets int, err error)
	Recording() (path string, packets int, on bool)
}

// captureMsg reports a recording starting or stopping.
type captureMsg struct {
	started bool
	path    string
	packets int
	err     error
	why     string // set when the UI stopped it rather than the user
}

// toggleCapture starts recording, or stops the recording that is running.
func (a *app) toggleCapture() tea.Cmd {
	c := a.opts.Capture
	if _, _, on := c.Recording(); on {
		return func() tea.Msg {
			path, n, err := c.Stop()
			return captureMsg{path: path, packets: n, err: err}
		}
	}
	iface := a.opts.Iface
	return func() tea.Msg {
		path, err := c.Start(iface)
		return captureMsg{started: true, path: path, err: err}
	}
}

// stopCaptureFor ends a recording for a reason of the UI's own, such as
// leaving the network it was recording. Called off the UI goroutine; nil
// when nothing was recording.
func stopCaptureFor(c Capture, why string) *captureMsg {
	if c == nil {
		return nil
	}
	if _, _, on := c.Recording(); !on {
		return nil
	}
	path, n, err := c.Stop()
	return &captureMsg{path: path, packets: n, err: err, why: why}
}

// captured puts the outcome in the log, saying what to do next.
func (a *app) captured(msg captureMsg) {
	ev := engine.ProbeEvent{Probe: "pcap", Kind: engine.KindInfo, At: time.Now()}
	switch {
	case msg.started && msg.err != nil:
		ev.Kind = engine.KindError
		ev.Message = fmt.Sprintf("could not start recording: %v", msg.err)
	case msg.started:
		ev.Message = fmt.Sprintf("recording every packet shoal sends and hears to %s; press r to rescan so the whole process is in it, and w again to stop", msg.path)
	default:
		ev.Message = fmt.Sprintf("saved %d packets to %s; open it in Wireshark, where each packet's comment is shoal's reading of it", msg.packets, msg.path)
		if msg.why != "" {
			ev.Message = fmt.Sprintf("stopped recording (%s): ", msg.why) + ev.Message
		}
		if msg.err != nil {
			ev.Kind = engine.KindError
			ev.Message += fmt.Sprintf("; but %v", msg.err)
		}
	}
	a.events = append(a.events, ev)
}

// recordingLabel is the status bar's reminder that packets are going to a
// file, with how many so far; empty when not recording.
func (a *app) recordingLabel() string {
	if a.opts.Capture == nil {
		return ""
	}
	_, n, on := a.opts.Capture.Recording()
	if !on {
		return ""
	}
	return fmt.Sprintf("REC %d", n)
}
