package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/BT10011/shoal/internal/config"
	"github.com/BT10011/shoal/internal/selfupdate"
	"github.com/BT10011/shoal/internal/store"
)

// releaseRepo is the GitHub repository --update fetches from. It is stamped
// in at build time beside the version, so a beta handed out from a public
// download-only repository updates from there rather than from the private
// source repository. See the release target in the Makefile.
var releaseRepo = selfupdate.DefaultRepo

// runUpdate replaces this binary with the latest published release.
func runUpdate(args []string) error {
	fs := flag.NewFlagSet("shoal --update", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "answer yes to the question about replacing a build made from source")
	check := fs.Bool("check", false, "say which release is latest and stop, changing nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := selfupdate.Options{
		Repo:    releaseRepo,
		BaseURL: os.Getenv("SHOAL_BASE_URL"),
		Version: version,
		Out:     os.Stdout,
		Confirm: confirmer(*yes),
	}
	if *check {
		latest, err := selfupdate.LatestVersion(context.Background(), opts)
		if err != nil {
			return err
		}
		if latest == version {
			fmt.Printf("shoal %s is the latest release.\n", version)
			return nil
		}
		fmt.Printf("shoal %s is installed; %s is the latest release.\nRun shoal --update to install it.\n", version, latest)
		return nil
	}
	return selfupdate.Update(context.Background(), opts)
}

// runUninstall removes shoal and everything it has written.
func runUninstall(args []string) error {
	fs := flag.NewFlagSet("shoal --uninstall", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	historyPath := fs.String("history", "", "a history file kept somewhere other than the default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := selfupdate.UninstallOptions{
		Out:     os.Stdout,
		Confirm: confirmer(*yes),
	}
	// Both paths come from the packages that own them, so an uninstall
	// cannot go looking in the wrong place if either ever moves.
	if p, err := config.DefaultPath(); err == nil {
		opts.ConfigFile = p
	}
	opts.HistoryFile = *historyPath
	if opts.HistoryFile == "" {
		if p, err := store.DefaultHistoryPath(); err == nil {
			opts.HistoryFile = p
		}
	}
	return selfupdate.Uninstall(opts)
}

// confirmer asks on the terminal, unless --yes already answered. A question
// with nobody to answer it — a pipe, a script — is a no, since both of
// these change the machine.
func confirmer(yes bool) func(string) bool {
	if yes {
		return func(string) bool { return true }
	}
	return func(question string) bool {
		fmt.Printf("%s [y/N] ", question)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			fmt.Println()
			return false
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		}
		return false
	}
}
