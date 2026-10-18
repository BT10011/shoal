package main

import (
	"context"
	"fmt"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/arp"
	"github.com/BT10011/shoal/internal/probe/av"
	"github.com/BT10011/shoal/internal/probe/fake"
	"github.com/BT10011/shoal/internal/probe/history"
	"github.com/BT10011/shoal/internal/probe/rogue"
	"github.com/BT10011/shoal/internal/store"
	"github.com/BT10011/shoal/internal/ui"
)

// runDemo runs shoal on the demo's invented networks: scripted probes, no
// privileges, no packets sent. i switches between the networks the demo
// machine is on, as it does on a real one.
func runDemo(theme string, saveTheme func(string) error) error {
	// The demo's history lives in memory, seeded with an invented visit
	// three days ago, so it shows what changed without touching the real file.
	h, err := store.OpenHistory("")
	if err != nil {
		return err
	}
	defer h.Close()
	if err := fake.SeedHistory(h, time.Now()); err != nil {
		return err
	}
	d := demo{history: h}
	first := fake.Interface()
	s, _, err := d.open(first, ui.SweepAsk)
	if err != nil {
		return err
	}
	return ui.Run(context.Background(), ui.Options{
		Store: s.Store, Engine: s.Engine, Iface: first, Mode: s.Mode, Demo: true, Theme: theme, SaveTheme: saveTheme,
		Interfaces: func() ([]netif.Interface, error) { return fake.Interfaces(), nil },
		Open:       d.open,
	})
}

// demo opens the demo's networks as scanConfig opens real ones, with
// scripted probes in place of the network's.
type demo struct {
	history *store.History // shared, so a network visited earlier is remembered
}

// open builds the scripted probes for one demo network. It refuses the
// interface, or asks about the subnet, wherever a real scan would.
func (d demo) open(iface netif.Interface, choice ui.SweepChoice) (ui.Session, *ui.LargeSweep, error) {
	if len(iface.MAC) != 6 {
		return ui.Session{}, nil, fmt.Errorf("cannot sweep %s with ARP: interface %q has no Ethernet MAC, IPv4 address or subnet", iface.Name, iface.Name)
	}
	if choice == ui.SweepAsk && iface.Hosts() > min(arp.DefaultMaxHosts, arp.LargestSweep) {
		return ui.Session{}, &ui.LargeSweep{Iface: iface, Hosts: iface.Hosts(), Limit: arp.DefaultMaxHosts,
			Largest: arp.LargestSweep, Rate: largeRate, Local: around(iface)}, nil
	}
	opts := fake.Options{Devices: fake.DevicesFor(iface)}
	if iface.Gateway != nil {
		opts.Resolver = iface.Gateway.String()
	}
	switch choice {
	case ui.SweepAll:
		opts.Interval = time.Second / largeRate // the pace of a real sweep of all of it
	case ui.SweepLocal:
		opts.Range = around(iface)
	case ui.SweepNone:
		opts.Listen = true
	}
	st := store.NewMemory()
	eng := engine.New(st, iface)
	if err := eng.AddDiscoverer(fake.NewDiscoverer(opts)); err != nil {
		return ui.Session{}, nil, err
	}
	if err := eng.AddDiscoverer(history.New(history.Options{History: d.history, Store: st, Status: eng.Status})); err != nil {
		return ui.Session{}, nil, err
	}
	ouiEnricher, err := realOUI()
	if err != nil {
		return ui.Session{}, nil, err
	}
	for _, en := range append([]engine.Enricher{ouiEnricher, rogue.New(iface.Subnet), av.New()}, fake.NewEnrichers(opts)...) {
		if err := eng.AddEnricher(en); err != nil {
			return ui.Session{}, nil, err
		}
	}
	return ui.Session{Store: st, Engine: eng, Iface: iface}, nil, nil
}
