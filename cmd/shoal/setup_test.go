package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/arp"
)

func TestUnprivilegedIsGoneAndSaysWhatToDoInstead(t *testing.T) {
	err := runTUI([]string{"--unprivileged"})
	if err == nil || !strings.Contains(err.Error(), "shoal --setup") || !strings.Contains(err.Error(), "shoal probe neigh") {
		t.Fatalf("err = %v, want --unprivileged refused with the alternatives named", err)
	}
}

func TestUpgradeIsTheSameCommandAsUpdate(t *testing.T) {
	// v1.0 copies know only --update; both names must reach the updater.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			http.Redirect(w, r, "/releases/tag/v9.9.9", http.StatusFound)
			return
		}
		fmt.Fprint(w, "a release page")
	}))
	defer srv.Close()
	t.Setenv("SHOAL_BASE_URL", srv.URL+"/releases")
	for _, args := range [][]string{{"--upgrade", "--check"}, {"-upgrade", "-check"}, {"--update", "--check"}} {
		out := captureStdout(t, func() {
			if err := run(args); err != nil {
				t.Errorf("run(%q) = %v", args, err)
			}
		})
		if !strings.Contains(out, "v9.9.9 is the latest release") {
			t.Errorf("run(%q) printed %q, want the latest release named", args, out)
		}
	}
}

func TestWithoutAccessAndNobodyToAskShoalDoesNotScan(t *testing.T) {
	// Tests run with no terminal, which is the case of shoal started by a
	// script: there is nobody to agree to setup, so it must stop and say
	// how to set up, rather than scan some lesser way.
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("needs Linux and an ordinary account, where raw sockets are refused")
	}
	lo := netif.Interface{Name: "lo", Index: 1}
	if arp.CheckAccess(lo) == nil {
		t.Skip("this test binary has raw network access")
	}
	t.Setenv(setupRestarted, "")
	err := ensureAccess(lo)
	if err == nil || !strings.Contains(err.Error(), "Run shoal --setup in a terminal") || !strings.Contains(err.Error(), "cap_net_raw") {
		t.Fatalf("ensureAccess = %v, want it to stop and say how to set up", err)
	}
}

func TestOpenRefusesAnInterfaceWithoutRawAccess(t *testing.T) {
	// The interface picker opens other interfaces through open; one that
	// refuses raw access is an error, never a reduced scan.
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("needs Linux and an ordinary account, where raw sockets are refused")
	}
	lo := testIface(t, "127.0.0.1", "127.0.0.0/8")
	lo.Name, lo.Index = "lo", 1
	if arp.CheckAccess(lo) == nil {
		t.Skip("this test binary has raw network access")
	}
	c := &scanConfig{}
	if _, _, err := c.open(lo, 0); err == nil || !strings.Contains(err.Error(), "cannot sweep lo with ARP") {
		t.Fatalf("open = %v, want a refusal", err)
	}
}

// captureStdout runs f and returns what it printed.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	f()
	w.Close()
	return <-done
}

// A question is answered only from a terminal: a "y" arriving down a pipe
// is never taken for agreement, since these questions change the machine.
func TestConfirmationIsNeverReadFromAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := w.WriteString("y\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	var out strings.Builder
	if confirmFrom(r, &out, false)("Remove all of this?") {
		t.Fatal("a piped y was taken as agreement")
	}
	if !strings.Contains(out.String(), "--yes") {
		t.Errorf("the refusal should say how a script agrees: %q", out.String())
	}
	if rest, _ := io.ReadAll(r); string(rest) != "y\n" {
		t.Errorf("the pipe was read: %q left", rest)
	}
	if !confirmFrom(r, &out, true)("Remove all of this?") {
		t.Error("--yes should agree without asking")
	}
}
