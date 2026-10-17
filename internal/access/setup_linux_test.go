package access

import (
	"strings"
	"testing"
)

func TestLinuxSetupGrantsTheCapabilityToTheBinary(t *testing.T) {
	setcap := SetcapPath()
	if setcap == "" {
		t.Skip("setcap is not installed here")
	}
	const binary = "/usr/local/bin/shoal"
	s := For(binary, nil)
	if !s.Automatic() {
		t.Fatalf("setup is not automatic with setcap at %s: %+v", setcap, s)
	}
	var got []string
	err := s.Apply(func(name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{setcap, "cap_net_raw+ep", binary}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("setup ran %q, want %q", got, want)
	}
	text := s.Describe()
	for _, want := range []string{binary, "cap_net_raw", "Setting it up will"} {
		if !strings.Contains(text, want) {
			t.Errorf("the description does not mention %q:\n%s", want, text)
		}
	}
}

func TestStillRefusedNamesTheLikelyCause(t *testing.T) {
	// A file with no capability: setcap did not take.
	msg := StillRefused(t.TempDir()+"/shoal", errRefused)
	if !strings.Contains(msg, "did not leave the cap_net_raw capability") {
		t.Errorf("StillRefused = %q, want it to say setcap did not take", msg)
	}
}

var errRefused = refusedError{}

type refusedError struct{}

func (refusedError) Error() string { return "AF_PACKET socket refused" }
