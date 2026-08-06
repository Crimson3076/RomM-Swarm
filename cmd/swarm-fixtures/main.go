// Command swarm-fixtures writes a sample library and a matching reference
// catalogue, so the verification pipeline can be exercised by hand without
// touching anyone's real collection.
//
// It writes structurally valid cartridge images — real headers, real checksums,
// real magic values — over deterministic filler. No copyrighted content is
// reproduced, and the same seed always produces the same bytes.
//
// The generated library deliberately includes the cases that break naive
// approaches: one game present as both a plain image and a copier image, a
// trimmed cartridge dump, a zipped ROM, a damaged dump, and a file that is not a
// ROM at all.
//
//	swarm-fixtures -out /tmp/sample
//	swarm-verify -dat /tmp/sample/catalogue.dat -platform gb -v /tmp/sample/library
package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Crimson3076/RomM-Swarm/internal/romfixture"
	"github.com/Crimson3076/RomM-Swarm/protocol"
)

func main() {
	out := flag.String("out", "sample", "directory to write the sample library and catalogue into")
	flag.Parse()

	libDir := filepath.Join(*out, "library")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		fail(err)
	}

	// Game Boy titles that will also appear in the catalogue, so the
	// classification path can be exercised.
	catalogued := map[string][]byte{
		"Smoke Quest (USA)":         romfixture.GameBoy("SMOKEQUEST", 65536, false),
		"Smoke Quest (Japan)":       romfixture.GameBoy("SMOKEQUESTJP", 65536, false),
		"Smoke Quest (USA) (Rev 1)": romfixture.GameBoy("SMOKEQUESTR1", 65536, false),
		"Zipped Quest (USA)":        romfixture.GameBoy("ZIPPEDQUEST", 65536, false),
	}

	files := map[string][]byte{}
	for name, data := range catalogued {
		files[name+".gb"] = data
	}

	// The zipped holding replaces its bare form: same canonical identity,
	// different stored identity.
	delete(files, "Zipped Quest (USA).gb")
	files["Zipped Quest (USA).zip"] = zipOf("Zipped Quest (USA).gb", catalogued["Zipped Quest (USA)"])

	// A damaged dump: still recognisably a cartridge, no longer the catalogued
	// one. Should classify as matched_unverified, never as verified.
	files["Damaged Quest (USA).gb"] = romfixture.Corrupt(catalogued["Smoke Quest (USA)"], 0x4000)

	// A holding with no catalogue entry at all.
	files["Uncatalogued (USA).gb"] = romfixture.GameBoy("UNCATALOGUED", 32768, false)

	// Other platforms, to exercise adapter selection.
	bin := romfixture.GenesisBin("SMOKE SEGA", 131072)
	smd, err := romfixture.GenesisSMD(bin)
	if err != nil {
		fail(err)
	}
	fullDS := romfixture.NintendoDS(romfixture.NintendoDSOptions{
		Title: "SMOKE DS", CapacityShift: 1, UsedBytes: 90000,
	})

	files["Smoke Color (USA).gbc"] = romfixture.GameBoy("SMOKECOLOR", 65536, true)
	files["Smoke Advance (USA).gba"] = romfixture.GameBoyAdvance("SMOKEADVANCE", 262144)
	files["Smoke DS (USA) [trimmed].nds"] = romfixture.NintendoDSTrimmed(fullDS)
	files["Smoke DS (USA).nds"] = fullDS
	files["Smoke Sega (USA).bin"] = bin
	files["Smoke Sega (USA).smd"] = smd
	files["notes.txt"] = []byte("not a ROM, and must not be claimed by any adapter\n")

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := os.WriteFile(filepath.Join(libDir, name), files[name], 0o644); err != nil {
			fail(err)
		}
	}

	datPath := filepath.Join(*out, "catalogue.dat")
	if err := os.WriteFile(datPath, buildDAT(catalogued), 0o644); err != nil {
		fail(err)
	}

	fmt.Printf("Wrote %d files to %s\n", len(files), libDir)
	fmt.Printf("Wrote a catalogue of %d entries to %s\n\n", len(catalogued), datPath)
	fmt.Printf("Try:\n")
	fmt.Printf("  swarm-verify %s\n", libDir)
	fmt.Printf("  swarm-verify -dat %s -platform gb -v %s\n", datPath, libDir)
}

// buildDAT renders a Logiqx catalogue with real hashes over the given payloads.
func buildDAT(entries map[string][]byte) []byte {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>` + "\n<datafile>\n")
	b.WriteString("  <header>\n    <name>Nintendo - Game Boy</name>\n" +
		"    <description>RomM Swarm sample catalogue, synthesised</description>\n" +
		"    <version>20260806-000000</version>\n" +
		"    <date>2026-08-06</date>\n" +
		"    <author>swarm-fixtures</author>\n  </header>\n")

	for _, name := range names {
		d := protocol.DigestBytes(entries[name])
		fmt.Fprintf(&b, "  <game name=%q>\n    <description>%s</description>\n"+
			"    <rom name=\"%s.gb\" size=\"%d\" crc=\"%s\" md5=\"%s\" sha1=\"%s\"/>\n  </game>\n",
			name, name, name, d.Size,
			strings.ToUpper(d.CRC32), strings.ToUpper(d.MD5), strings.ToUpper(d.SHA1))
	}
	b.WriteString("</datafile>\n")
	return []byte(b.String())
}

func zipOf(name string, data []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		fail(err)
	}
	if _, err := w.Write(data); err != nil {
		fail(err)
	}
	if err := zw.Close(); err != nil {
		fail(err)
	}
	return buf.Bytes()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "swarm-fixtures: "+err.Error())
	os.Exit(1)
}
