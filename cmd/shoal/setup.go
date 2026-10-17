package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/BT10011/shoal/internal/access"
	"github.com/BT10011/shoal/internal/elevate"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/arp"
)

// setupRestarted marks a shoal that setup started again to pick its change
// up, so a refusal setup did not cure is explained rather than set up a
// second time.
const setupRestarted = "SHOAL_SETUP_RESTARTED"

// runSetup is shoal --setup: give shoal full network access, or say it has
// it already. Start-up offers the same when access is missing.
func runSetup(args []string) error {
	fs := flag.NewFlagSet("shoal --setup", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "make the changes without asking first")
	ifaceName := fs.String("iface", "", "interface to check access on (default: the one carrying the default route)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	binary, err := programPath()
	if err != nil {
		return err
	}
	if elevate.IsRoot() && runtime.GOOS != "windows" {
		return setupAsRoot(binary, *yes)
	}
	iface, err := setupInterface(*ifaceName)
	if err != nil {
		return err
	}
	refusal := arp.CheckAccess(iface)
	if os.Getenv(setupRestarted) != "" {
		// Setup has made its change and started shoal again to check it.
		s := access.For(binary, refusal)
		if refusal != nil {
			refusal = settle(iface, s.Settle)
		}
		if refusal != nil {
			return errors.New(access.StillRefused(binary, refusal))
		}
		fmt.Printf("Done: shoal has full network access now, and can sweep %s with ARP.\nRun shoal to scan.\n", iface.Name)
		return nil
	}
	switch {
	case refusal == nil:
		fmt.Printf("Shoal already has full network access: it can sweep %s with ARP.\nThere is nothing to set up.\n", iface.Name)
		return nil
	case !errors.Is(refusal, arp.ErrPermission):
		return fmt.Errorf("shoal cannot sweep %s with ARP, and it is not a matter of permission, so setup cannot help: %w", iface.Name, refusal)
	}
	if err := offerSetup(access.For(binary, refusal), confirmer(*yes)); err != nil {
		return err
	}
	return restart()
}

// setupAsRoot is shoal --setup run with sudo: there is no checking whether
// root can sweep, since root always can, so the changes are made for the
// account that ran sudo, and checked when it runs shoal itself.
func setupAsRoot(binary string, yes bool) error {
	who := os.Getenv("SUDO_USER")
	if who == "" || who == "root" {
		fmt.Println("Root needs no setup: it can always sweep with ARP. To set shoal up for your own\naccount, run shoal --setup without sudo.")
		return nil
	}
	s := access.For(binary, nil)
	if !s.Automatic() {
		fmt.Println("There is nothing setup can change here. " + s.Manual)
		return nil
	}
	if err := offerSetup(s, confirmer(yes)); err != nil {
		return err
	}
	fmt.Printf("Done. Run shoal as %s, without sudo, to scan.\n", who)
	return nil
}

// ensureAccess makes sure the sweep can run on iface before shoal starts.
// There is no reduced mode: when raw packet access is refused, shoal says
// what is missing and what setting it up changes, makes the change once
// the user agrees, and starts itself again to use it. Declined, or with
// nobody at the terminal to ask, shoal does not scan.
func ensureAccess(iface netif.Interface) error {
	refusal := arp.CheckAccess(iface)
	if refusal == nil {
		return nil
	}
	if !errors.Is(refusal, arp.ErrPermission) {
		return fmt.Errorf("cannot sweep %s with ARP: %w", iface.Name, refusal)
	}
	binary, err := programPath()
	if err != nil {
		return err
	}
	s := access.For(binary, refusal)
	if os.Getenv(setupRestarted) != "" {
		if refusal = settle(iface, s.Settle); refusal == nil {
			return nil
		}
		return errors.New(access.StillRefused(binary, refusal))
	}
	if !interactive() {
		return fmt.Errorf("shoal needs full network access to scan, and has not got it.\n\n%s\nRun shoal --setup in a terminal to set it up", s.Describe())
	}
	if err := offerSetup(s, confirmer(false)); err != nil {
		return err
	}
	return restart()
}

// offerSetup shows what is missing and what setup changes, and makes the
// changes once the user agrees. When the user has to act instead, as with
// Npcap on Windows, it opens the page and waits for them.
func offerSetup(s access.Setup, confirm func(string) bool) error {
	fmt.Print(s.Describe())
	fmt.Println()
	if s.Automatic() {
		question := "Set it up now? sudo will ask for your password."
		if elevate.IsRoot() {
			question = "Set it up now?"
		}
		if !confirm(question) {
			return errors.New("nothing was changed. Shoal needs full network access to scan: run shoal --setup when you are ready, or shoal --demo to look around first")
		}
		if err := s.Apply(elevate.Run); err != nil {
			return fmt.Errorf("setup did not finish: %w", err)
		}
		return nil
	}
	if s.URL == "" {
		return errors.New("shoal needs full network access to scan; see above for what to do")
	}
	if confirm("Open " + s.URL + " in your browser now?") {
		if err := access.Open(s.URL); err != nil {
			fmt.Printf("It could not be opened (%v); go to %s yourself.\n", err, s.URL)
		}
	}
	fmt.Print("\nPress Enter once that is done, and shoal will check again. ")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		fmt.Println()
		return errors.New("there is nobody at the terminal to wait for; start shoal again once it is done")
	}
	return nil
}

// settle checks access again for up to seconds, since macOS can take a
// moment to apply a new group membership to a running login.
func settle(iface netif.Interface, seconds int) error {
	refusal := arp.CheckAccess(iface)
	for deadline := time.Now().Add(time.Duration(seconds) * time.Second); refusal != nil && time.Now().Before(deadline); {
		time.Sleep(500 * time.Millisecond)
		refusal = arp.CheckAccess(iface)
	}
	return refusal
}

// setupInterface is the interface setup checks access on: the one asked
// for, else the one carrying the default route, else the first one up, so
// setup works on a machine with no route out.
func setupInterface(name string) (netif.Interface, error) {
	if name != "" {
		return pickInterface(name)
	}
	if iface, err := pickInterface(""); err == nil {
		return iface, nil
	}
	list, err := netif.Candidates()
	if err != nil {
		return netif.Interface{}, err
	}
	if len(list) == 0 {
		return netif.Interface{}, errors.New("no network interface is up to check access on; connect to a network and run shoal --setup again")
	}
	return list[0], nil
}

// interactive reports whether there is someone at a terminal to ask.
func interactive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		fi, err := f.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

// programPath is the running program's file, with symlinks resolved: the
// file a capability is granted to.
func programPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find the running shoal program: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}
