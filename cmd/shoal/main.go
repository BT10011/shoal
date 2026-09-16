// Command shoal is a terminal LAN discovery tool that shows its work.
package main

import (
	"fmt"
	"os"

	"github.com/BT10011/shoal/internal/netif"
)

const usage = `shoal — LAN discovery that shows its work

Usage:
  shoal iface [name]     Show the interface, subnet and gateway shoal would scan
  shoal probe <name>     Run one probe standalone and print what it sends, receives and learns

Only scan networks you own or are authorised to test.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "shoal:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("no command given")
	}
	switch args[0] {
	case "iface":
		return runIface(args[1:])
	case "probe":
		return runProbe(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runIface(args []string) error {
	var (
		i   netif.Interface
		err error
	)
	if len(args) > 0 {
		i, err = netif.ByName(args[0])
	} else {
		i, err = netif.Default()
	}
	if err != nil {
		return err
	}
	fmt.Print(i.Describe())
	return nil
}
