package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/capture"
)

// captureFixtureCmd is `aintercept capture-fixture`: one call from the raw
// files as an anonymized corpus case (docs/PROVIDERS.md, "Adding a case").
// With -list it lists the calls instead, to find the one to take.
func captureFixtureCmd() {
	fs := flag.NewFlagSet("capture-fixture", flag.ExitOnError)
	raw := fs.String("raw", "", "a raw file, or a directory of them (v9 raw/…, or the older capture layout)")
	call := fs.String("call", "", "the call's correlation id")
	turns := fs.String("turns", "", "a WebSocket's turns to keep, e.g. 2 or 1,2 (default: all)")
	out := fs.String("out", "", "the case's directory, e.g. aintercept/internal/events/testdata/captures/anthropic/claude-code/model-stream")
	shape := fs.String("shape", "", "the traffic shape the case represents (default: the directory's name)")
	notes := fs.String("notes", "", "anything a reader needs to know (what was cut, why this call)")
	deny := fs.String("denylist", "", "append the hashes of the real identifiers it replaced to this file (scripts/real-data-denylist.sha256)")
	trim := fs.Int("trim", 0, "cut each JSON array in the bodies longer than 2×N elements to its first and last N (for a shape that doesn't depend on length; noted in meta.json)")
	list := fs.Bool("list", false, "list the calls in -raw: id, host, method, path, status, sizes")
	fs.Parse(os.Args[2:])
	if *raw == "" || !*list && (*call == "" || *out == "") {
		fs.Usage()
		os.Exit(2)
	}
	if *list {
		if err := capture.List(*raw, os.Stdout); err != nil {
			fatalf("capture-fixture: %v", err)
		}
		return
	}
	o := capture.Options{Call: *call, Shape: *shape, Notes: *notes, Trim: *trim}
	if o.Shape == "" {
		o.Shape = filepath.Base(*out)
	}
	for _, t := range strings.Split(*turns, ",") {
		if t = strings.TrimSpace(t); t != "" {
			n, err := strconv.Atoi(t)
			if err != nil {
				fatalf("capture-fixture: -turns: %v", err)
			}
			o.Turns = append(o.Turns, n)
		}
	}
	c, a, err := capture.FromRaw(*raw, o)
	if err != nil {
		fatalf("capture-fixture: %v", err)
	}
	if err := c.Write(*out); err != nil {
		fatalf("capture-fixture: %v", err)
	}
	if err := capture.CheckDir(*out, a); err != nil {
		os.RemoveAll(*out)
		fatalf("capture-fixture: %v (the case was not written)", err)
	}
	if *deny != "" {
		if err := appendHashes(*deny, a.Who); err != nil {
			fatalf("capture-fixture: %v", err)
		}
	}
	fmt.Printf("wrote %s (%d identifiers replaced); now write its expect.json\n", *out, len(a.Replaced()))
}

// appendHashes adds the hashes of the real identifiers (lowercased, as
// scripts/check-real-data.py hashes them) that the denylist doesn't have.
func appendHashes(path string, who map[string]bool) error {
	have := map[string]bool{}
	if b, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			have[strings.TrimSpace(l)] = true
		}
	}
	var add []string
	for w := range who {
		if len(w) < 7 {
			continue
		}
		sum := sha256.Sum256([]byte(strings.ToLower(w)))
		if h := hex.EncodeToString(sum[:]); !have[h] {
			have[h] = true
			add = append(add, h)
		}
	}
	sort.Strings(add)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, h := range add {
		if _, err := fmt.Fprintln(f, h); err != nil {
			return err
		}
	}
	return nil
}
