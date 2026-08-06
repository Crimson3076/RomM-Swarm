// Command swarm-verify computes the four identities for files on disk, and
// optionally classifies them against a reference catalogue.
//
// It is the hands-on counterpart to the verification harness: point it at a
// directory of ROMs and it reports, for each one, what the stored file is, what
// the canonical payload is, which adapter produced it, and whether it matches an
// approved reference entry under a collection profile.
//
// It never writes to the files it reads.
//
//	swarm-verify ~/roms/gb
//	swarm-verify -dat "Nintendo - Game Boy.dat" -platform gb ~/roms/gb
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/protocol"
	"github.com/Crimson3076/RomM-Swarm/reference"
	"github.com/Crimson3076/RomM-Swarm/verify"
)

func main() {
	datPath := flag.String("dat", "", "reference catalogue to classify against (Logiqx XML)")
	platform := flag.String("platform", "", "platform key for the catalogue, for example gb")
	verbose := flag.Bool("v", false, "print every observation, not just the summary line")
	flag.Parse()

	paths := flag.Args()
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: swarm-verify [-dat catalogue.dat] [-platform gb] path...")
		os.Exit(1)
	}

	var sel *reference.Selection
	if *datPath != "" {
		f, err := os.Open(*datPath)
		if err != nil {
			fail(err.Error())
		}
		set, err := reference.ImportDAT(f, reference.ImportOptions{
			Platform: protocol.PlatformID(*platform),
		})
		f.Close()
		if err != nil {
			fail(err.Error())
		}
		profile := reference.DefaultProfile()
		sel = profile.Apply(set)
		fmt.Printf("Reference: %s\n", set.Ref())
		fmt.Printf("Profile:   %s (%d entries expected)\n\n", profile.Ref(), sel.Size())
	}

	files := collect(paths)
	analyzer := &verify.Analyzer{}
	counts := map[string]int{}

	for _, path := range files {
		res, err := analyzer.AnalyzeFile(path, protocol.PlatformID(*platform))
		if err != nil {
			fmt.Printf("%-50s error: %v\n", filepath.Base(path), err)
			counts["error"]++
			continue
		}

		label := "unrecognised"
		if res.Canonicalized {
			label = fmt.Sprintf("%s via %s", res.Platform, res.Adapter)
		}

		if sel != nil {
			out := reference.Classify(res, sel, "")
			label = string(out.Classification)
			counts[label]++
			fmt.Printf("%-50s %-20s %s\n", truncate(filepath.Base(path), 50), label, canonicalSummary(res))
			if *verbose {
				for _, n := range append(res.Notes, out.Notes...) {
					fmt.Printf("    %s\n", n)
				}
			}
			continue
		}

		counts[label]++
		fmt.Printf("%-50s %-28s stored:%s canonical:%s\n",
			truncate(filepath.Base(path), 50), label,
			short(res.Stored.SHA256), short(res.Canonical.SHA256))
		if *verbose {
			for _, n := range res.Notes {
				fmt.Printf("    %s\n", n)
			}
		}
	}

	fmt.Printf("\n%d files\n", len(files))
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-24s %d\n", k, counts[k])
	}
}

// collect walks the given paths and returns every regular file.
func collect(paths []string) []string {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fail(err.Error())
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			fail(err.Error())
		}
	}
	sort.Strings(out)
	return out
}

// canonicalSummary describes the canonical payload, or says plainly that there
// is none. Printing a zero-valued digest would read as "0 bytes", which is a
// statement about the file rather than about the analysis.
func canonicalSummary(res *verify.Result) string {
	if !res.Canonicalized {
		return fmt.Sprintf("no canonical payload (stored %d bytes)", res.Stored.Size)
	}
	return res.Canonical.String()
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "swarm-verify: "+strings.TrimSpace(msg))
	os.Exit(1)
}
