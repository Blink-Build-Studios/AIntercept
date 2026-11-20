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
	deny := fs.String("denylist", "", "append the hashes of the random identifiers it replaced (UUIDs, long device and session ids) to this committed file (scripts/real-data-denylist.sha256)")
	denyLocal := fs.String("denylist-local", "", "append the hashes of the guessable ones (emails, IPs, names, hosts, short ids) to this gitignored file (default: data/real-data-denylist.local.sha256 in -denylist's repo)")
	corpus := fs.String("corpus", "", "the corpus root, whose model calls' model names are kept (default: three levels above -out)")
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
	if *corpus == "" {
		*corpus = filepath.Dir(filepath.Dir(filepath.Dir(filepath.Clean(*out))))
	}
	if _, err := os.Stat(*corpus); err == nil {
		models, err := capture.CorpusModels(*corpus)
		if err != nil {
			fatalf("capture-fixture: -corpus: %v", err)
		}
		o.Models = models
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
	// Checked in a directory of its own first: a case being captured again
	// keeps its expect.json, and its old files when the check fails.
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fatalf("capture-fixture: %v", err)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(*out), ".capture-fixture-")
	if err != nil {
		fatalf("capture-fixture: %v", err)
	}
	err = c.Write(tmp)
	if err == nil {
		err = capture.CheckDir(tmp, a)
	}
	os.RemoveAll(tmp)
	if err != nil {
		fatalf("capture-fixture: %v (the case was not written)", err)
	}
	if err := c.Write(*out); err != nil {
		fatalf("capture-fixture: %v", err)
	}
	if *deny != "" {
		if *denyLocal == "" {
			abs, err := filepath.Abs(*deny)
			if err != nil {
				fatalf("capture-fixture: %v", err)
			}
			*denyLocal = filepath.Join(filepath.Dir(filepath.Dir(abs)), "data", "real-data-denylist.local.sha256")
		}
		if err := appendHashes(*deny, a.Who, capture.HighEntropy); err != nil {
			fatalf("capture-fixture: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(*denyLocal), 0o755); err != nil {
			fatalf("capture-fixture: %v", err)
		}
		if err := appendHashes(*denyLocal, a.Who, func(s string) bool { return !capture.HighEntropy(s) }); err != nil {
			fatalf("capture-fixture: %v", err)
		}
	}
	fmt.Printf("wrote %s (%d identifiers replaced); now write its expect.json\n", *out, len(a.Replaced()))
}

// appendHashes adds the hashes of the real identifiers (lowercased, as
// scripts/check-real-data.py hashes them) that pass and that the denylist
// doesn't have.
func appendHashes(path string, who map[string]bool, pass func(string) bool) error {
	have := map[string]bool{}
	if b, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			have[strings.TrimSpace(l)] = true
		}
	}
	var add []string
	for w := range who {
		if len(w) < 7 || !pass(w) {
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
