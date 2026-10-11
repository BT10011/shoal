package main

import (
	"flag"
	"net"
	"strings"
	"testing"

	"github.com/BT10011/shoal/internal/config"
	"github.com/BT10011/shoal/internal/netif"
)

func parse(t *testing.T, args ...string) (*flag.FlagSet, string) {
	t.Helper()
	fs := flag.NewFlagSet("shoal", flag.ContinueOnError)
	theme := fs.String("theme", "catppuccin-mocha", "")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return fs, *theme
}

func TestThemeSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	fs, flagTheme := parse(t)
	name, save := themeSettings(fs, flagTheme)
	if name != "catppuccin-mocha" || save == nil {
		t.Fatalf("nothing saved yet: %q, save=%v", name, save != nil)
	}
	if err := save("nord"); err != nil {
		t.Fatal(err)
	}

	fs, flagTheme = parse(t)
	if name, _ := themeSettings(fs, flagTheme); name != "nord" {
		t.Fatalf("the kept theme should be used next time: %q", name)
	}

	fs, flagTheme = parse(t, "--theme", "vt100")
	if name, _ := themeSettings(fs, flagTheme); name != "vt100" {
		t.Fatalf("--theme wins for this run: %q", name)
	}
	path, _ := config.DefaultPath()
	if c, _ := config.Load(path); c.Theme != "nord" {
		t.Fatalf("--theme must not replace the remembered theme: %q", c.Theme)
	}
}

func testIface(t *testing.T, ip, cidr string) netif.Interface {
	t.Helper()
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return netif.Interface{Name: "eth0", IP: net.ParseIP(ip).To4(), Subnet: subnet}
}

func TestLargeAsksAboveTheLimitAndUpToASlash16(t *testing.T) {
	c := &scanConfig{}
	if l := c.large(testIface(t, "172.16.10.5", "172.16.8.0/22")); l != nil {
		t.Errorf("a /22 is swept without asking: %+v", l)
	}
	l := c.large(testIface(t, "172.16.10.5", "172.16.0.0/16"))
	if l == nil {
		t.Fatal("a /16 should be asked about")
	}
	if l.Hosts != 65534 || l.Limit != 1022 || l.Rate != largeRate || l.Local.String() != "172.16.10.0/24" {
		t.Errorf("large = %+v, local %s", l, l.Local)
	}

	// --max-hosts raised to cover it: the user said so on the command line.
	c = &scanConfig{maxHosts: 65534, rate: 250}
	if l := c.large(testIface(t, "172.16.10.5", "172.16.0.0/16")); l != nil {
		t.Errorf("--max-hosts 65534 covers a /16: %+v", l)
	}
	// Above a /16 it is always asked about, so the /24 can still be chosen.
	l = c.large(testIface(t, "10.20.30.40", "10.0.0.0/8"))
	if l == nil || l.Rate != 250 || l.Local.String() != "10.20.30.0/24" {
		t.Errorf("a /8 should be asked about at the --rate given: %+v", l)
	}
}

func TestUnknownThemeIsRefusedWithTheChoices(t *testing.T) {
	err := runTUI([]string{"--theme", "no-such-theme", "--demo"})
	if err == nil || !strings.Contains(err.Error(), `no theme called "no-such-theme"`) || !strings.Contains(err.Error(), "kanagawa-wave") {
		t.Fatalf("err = %v, want the unknown name refused and the choices listed", err)
	}
}
