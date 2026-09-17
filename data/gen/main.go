// Command gen refreshes oui.csv from the IEEE MA-L registry, keeping only
// the assignment prefix and organisation name.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const sourceURL = "https://standards-oui.ieee.org/oui/oui.csv"

func main() {
	from := flag.String("from", "", "read the raw IEEE CSV from this file instead of downloading it")
	out := flag.String("out", "oui.csv", "output path")
	flag.Parse()
	if err := run(*from, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(from, out string) error {
	var src io.ReadCloser
	if from != "" {
		f, err := os.Open(from)
		if err != nil {
			return err
		}
		src = f
	} else {
		resp, err := http.Get(sourceURL)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("download %s: %s", sourceURL, resp.Status)
		}
		src = resp.Body
	}
	defer src.Close()

	r := csv.NewReader(src)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return err
	}
	if len(header) < 3 || header[0] != "Registry" || header[1] != "Assignment" {
		return fmt.Errorf("unexpected header %q", header)
	}

	type entry struct{ prefix, org string }
	var entries []entry
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(rec) < 3 || rec[0] != "MA-L" {
			continue
		}
		prefix := strings.ToUpper(strings.TrimSpace(rec[1]))
		org := strings.Join(strings.Fields(rec[2]), " ")
		if len(prefix) != 6 || org == "" {
			continue
		}
		entries = append(entries, entry{prefix, org})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].prefix < entries[j].prefix })

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "# IEEE MA-L registry, %s, fetched %s\n", sourceURL, time.Now().UTC().Format("2006-01-02"))
	w := csv.NewWriter(f)
	for _, e := range entries {
		if err := w.Write([]string{e.prefix, e.org}); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	fmt.Printf("wrote %d MA-L assignments to %s\n", len(entries), out)
	return nil
}
