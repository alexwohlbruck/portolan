package pipeline

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexwohlbruck/portolan/internal/style"
)

func TestBuildFaresNilAndPriceless(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *style.Fares
	}{
		{"nil", nil},
		{"zero price", &style.Fares{Currency: "USD"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildFares(tc.in)
			if err != nil {
				t.Fatalf("buildFares: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("want no tables, got %d", len(got))
			}
		})
	}
}

// A price with no transfer allowance charges every leg: there must be no
// transfer rule at all, because an empty one would read as "free".
func TestBuildFaresNoTransfer(t *testing.T) {
	got, err := buildFares(&style.Fares{Currency: "USD", Price: 2.90})
	if err != nil {
		t.Fatalf("buildFares: %v", err)
	}
	if _, ok := got["fare_transfer_rules.txt"]; ok {
		t.Fatal("a tariff with no transfer allowance must emit no transfer rule")
	}
	prod := got["fare_products.txt"]
	if !strings.Contains(prod, "2.9,USD") {
		t.Fatalf("fare_products.txt missing price: %q", prod)
	}
	legs := got["fare_leg_rules.txt"]
	if !strings.Contains(legs, fareLegGroupID) || !strings.Contains(legs, fareProductID) {
		t.Fatalf("fare_leg_rules.txt does not bind legs to the product: %q", legs)
	}
}

// The MTA's shape: one fare, one free transfer, two-hour window. The
// window must be written in SECONDS — GTFS duration_limit is seconds, and
// a tariff that says 120 there would grant two minutes, not two hours.
func TestBuildFaresFreeTransferWindowInSeconds(t *testing.T) {
	got, err := buildFares(&style.Fares{
		Currency: "USD", Price: 2.90,
		Transfer: &style.FareTransfer{Count: 1, Minutes: 120},
	})
	if err != nil {
		t.Fatalf("buildFares: %v", err)
	}
	rule := got["fare_transfer_rules.txt"]
	lines := strings.Split(strings.TrimSpace(rule), "\n")
	if len(lines) != 2 {
		t.Fatalf("want header + 1 rule, got %d lines: %q", len(lines), rule)
	}
	cells := strings.Split(lines[1], ",")
	// from_leg_group, to_leg_group, transfer_count, duration_limit,
	// duration_limit_type, fare_transfer_type, fare_product_id
	if cells[2] != "1" {
		t.Errorf("transfer_count = %q, want 1", cells[2])
	}
	if cells[3] != "7200" {
		t.Errorf("duration_limit = %q, want 7200 seconds (120 min)", cells[3])
	}
	if cells[6] != "" {
		t.Errorf("fare_product_id = %q, want empty — a free transfer carries no product", cells[6])
	}
}

// Unlimited transfers within a window is -1, and must survive as -1 rather
// than being clamped to a count.
func TestBuildFaresUnlimitedTransfers(t *testing.T) {
	got, err := buildFares(&style.Fares{
		Currency: "USD", Price: 1.75,
		Transfer: &style.FareTransfer{Count: -1},
	})
	if err != nil {
		t.Fatalf("buildFares: %v", err)
	}
	rule := got["fare_transfer_rules.txt"]
	cells := strings.Split(strings.Split(strings.TrimSpace(rule), "\n")[1], ",")
	if cells[2] != "-1" {
		t.Errorf("transfer_count = %q, want -1", cells[2])
	}
	// No window given: both duration columns stay empty, which is how
	// Fares v2 spells "no limit".
	if cells[3] != "" || cells[4] != "" {
		t.Errorf("duration columns = %q/%q, want empty", cells[3], cells[4])
	}
}

func TestBuildFaresRejectsPriceWithoutCurrency(t *testing.T) {
	if _, err := buildFares(&style.Fares{Price: 2.90}); err == nil {
		t.Fatal("a price with no currency must be an error, not a silent no-op")
	}
}

// A curated tariff replaces whatever the feed shipped. A feed carrying a
// stale fare_products.txt must not end up with both.
func TestRewriteZipReplacesFeedFareTables(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.zip")
	writeTestZip(t, src, map[string]string{
		"agency.txt":        "agency_id,agency_name\nA,Test\n",
		"fare_products.txt": "fare_product_id,amount,currency\nold,9.99,USD\n",
	})

	fares, err := buildFares(&style.Fares{
		Currency: "USD", Price: 2.90,
		Transfer: &style.FareTransfer{Count: 1, Minutes: 120},
	})
	if err != nil {
		t.Fatalf("buildFares: %v", err)
	}

	dst := filepath.Join(dir, "out.zip")
	if err := rewriteZip(src, dst, nil, nil, fares); err != nil {
		t.Fatalf("rewriteZip: %v", err)
	}

	got := readTestZip(t, dst)
	if n := strings.Count(strings.Join(namesOf(got), " "), "fare_products.txt"); n != 1 {
		t.Fatalf("want exactly one fare_products.txt, got %d", n)
	}
	if strings.Contains(got["fare_products.txt"], "9.99") {
		t.Error("the feed's own fare survived a curated tariff")
	}
	if !strings.Contains(got["fare_products.txt"], "2.9") {
		t.Errorf("curated price missing: %q", got["fare_products.txt"])
	}
	if _, ok := got["fare_transfer_rules.txt"]; !ok {
		t.Error("fare_transfer_rules.txt was not written")
	}
	if got["agency.txt"] == "" {
		t.Error("unrelated files must pass through untouched")
	}
}

// With no curated tariff the feed's own fare data is left exactly as it was.
func TestRewriteZipKeepsFeedFaresWhenUncurated(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.zip")
	writeTestZip(t, src, map[string]string{
		"fare_products.txt": "fare_product_id,amount,currency\nown,3.50,EUR\n",
	})
	dst := filepath.Join(dir, "out.zip")
	if err := rewriteZip(src, dst, nil, nil, nil); err != nil {
		t.Fatalf("rewriteZip: %v", err)
	}
	if got := readTestZip(t, dst)["fare_products.txt"]; !strings.Contains(got, "3.50,EUR") {
		t.Errorf("feed's own fares were disturbed: %q", got)
	}
}

func namesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func writeTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func readTestZip(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string]string{}
	for _, e := range zr.File {
		r, err := e.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(e.Name)] = string(b)
	}
	return out
}
