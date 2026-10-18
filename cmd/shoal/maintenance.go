package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
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
	fs := flag.NewFlagSet("shoal --upgrade", flag.ContinueOnError)
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
		if selfupdate.Ahead(version, latest) {
			fmt.Printf("shoal %s is newer than the latest release, %s.\n", version, latest)
			return nil
		}
		fmt.Printf("shoal %s is installed; %s is the latest release.\nRun shoal --upgrade to install it.\n", version, latest)
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
		Yes:     *yes,
	}
	// Both paths come from the packages that own them, so an uninstall
	// cannot go looking in the wrong place if either ever moves.
	if p, err := config.DefaultPath(); err == nil {
		opts.ConfigFile = p
	}
	opts.HistoryFile, opts.HistoryNamed = *historyPath, *historyPath != ""
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
	return confirmFrom(os.Stdin, os.Stdout, yes)
}

// confirmFrom is confirmer reading the answer from in and asking on out.
// Only a terminal can answer: piped input is never taken for agreement.
func confirmFrom(in *os.File, out io.Writer, yes bool) func(string) bool {
	if yes {
		return func(string) bool { return true }
	}
	return func(question string) bool {
		fmt.Fprintf(out, "%s [y/N] ", question)
		if fi, err := in.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			fmt.Fprintln(out, "\nThere is no terminal to answer from, so that is a no. In a script, add --yes to agree.")
			return false
		}
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil {
			fmt.Fprintln(out)
			return false
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		}
		return false
	}
}
