package main

import (
	"strings"
	"testing"
)

// The line format is the whole contract with `go test -bench`, and it has one
// trap in it: the -N suffix is GOMAXPROCS, and Go omits it when N is 1. The
// bench workflow runs with -cpu 1 and nothing here did, so a regex requiring
// the suffix matched every line in local use and none in CI -- on all six
// runners at once, which looked like a runner problem and was not.
func TestLineMatchesBothCPUForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // the benchmark name the regex should capture
	}{
		{"with GOMAXPROCS suffix",
			"BenchmarkCompare64/4/haste-xxh3-16         \t 1351994\t         5.380 ns/op\t   0.74 MB/s",
			"Compare64/4/haste-xxh3"},
		{"cpu 1, no suffix",
			"BenchmarkCompare64/4/haste-xxh3         \t  823315\t         7.101 ns/op\t   0.56 MB/s",
			"Compare64/4/haste-xxh3"},
		{"no size element",
			"BenchmarkCompareStream/zeebo-8   \t     132\t     48402 ns/op",
			"CompareStream/zeebo"},
		{"integer ns, no decimals",
			"BenchmarkSum64/8-4   \t     132\t     48402 ns/op",
			"Sum64/8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := line.FindStringSubmatch(c.in)
			if m == nil {
				t.Fatalf("no match for %q", c.in)
			}
			if m[1] != c.want {
				t.Errorf("captured %q, want %q", m[1], c.want)
			}
		})
	}
}

// Lines that are not results must not match: a log is fed in whole.
func TestLineIgnoresNonResults(t *testing.T) {
	for _, s := range []string{
		"ok  \tgithub.com/JohanLindvall/haste/bench\t12.3s",
		"PASS",
		"goos: linux",
		"BenchmarkFoo-8   \t     132\t     48402 B/op", // not a timing line
		"# github.com/JohanLindvall/haste/bench",
	} {
		if line.MatchString(s) {
			t.Errorf("matched a non-result line: %q", s)
		}
	}
}

// split decides which path element is the size and which the implementation,
// which is what puts a number on the correct axis of the table.
func TestSplitCoordinates(t *testing.T) {
	cases := []struct {
		name              string
		group, size, impl string
	}{
		{"Compare64/4/haste-xxh3", "Compare64", "4", "haste-xxh3"},
		{"CompareStream/zeebo", "CompareStream", "", "zeebo"},
		{"Sum64/1024", "Sum64", "1024", "ns/op"},
		{"Backends/scalar/256", "Backends", "256", "scalar"},
	}
	for _, c := range cases {
		got := split(c.name, 1)
		if got.group != c.group || got.size != c.size || got.impl != c.impl {
			t.Errorf("split(%q) = {%q %q %q}, want {%q %q %q}",
				c.name, got.group, got.size, got.impl, c.group, c.size, c.impl)
		}
	}
}

// A whole run, end to end: two implementations at two sizes, three
// repetitions each, and the median of each cell with the winner in bold.
func TestEmitTable(t *testing.T) {
	in := strings.Join([]string{
		"goos: linux",
		"BenchmarkX/8/alpha   \t 100\t 10.00 ns/op",
		"BenchmarkX/8/alpha   \t 100\t 30.00 ns/op",
		"BenchmarkX/8/alpha   \t 100\t 20.00 ns/op", // median 20
		"BenchmarkX/8/beta    \t 100\t 15.00 ns/op",
		"BenchmarkX/16/alpha  \t 100\t 40.00 ns/op",
		"BenchmarkX/16/beta   \t 100\t 90.00 ns/op",
		"PASS",
	}, "\n")

	var sb strings.Builder
	if err := run(strings.NewReader(in), &sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		"| size | alpha | beta |",
		"| 8 | 20.00 | **15.00** |", // median of alpha, beta the winner
		"| 16 | **40.00** | 90.00 |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

// Nothing to render is an error, because the only way to get here is a
// pipeline that was supposed to produce benchmarks and did not.
func TestEmptyInputIsAnError(t *testing.T) {
	var sb strings.Builder
	if err := run(strings.NewReader("PASS\nok  \tpkg\t1s\n"), &sb); err == nil {
		t.Error("no error for input with no benchmark lines")
	}
}

// A single-implementation table has no winner to mark. Bolding the only cell
// of every row was pure noise, and made the one-column tables in
// benchmarks.md look like each value had beaten something.
func TestSingleColumnIsNotBolded(t *testing.T) {
	in := "BenchmarkSum64Seed/8   \t 100\t 4.20 ns/op\nBenchmarkSum64Seed/64  \t 100\t 6.34 ns/op\n"
	var sb strings.Builder
	if err := run(strings.NewReader(in), &sb); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sb.String(), "**") {
		t.Errorf("single-column table has bold cells:\n%s", sb.String())
	}
	for _, want := range []string{"| 8 | 4.20 |", "| 64 | 6.34 |"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("missing %q in:\n%s", want, sb.String())
		}
	}
}

// With two implementations the winner is still marked.
func TestTwoColumnsStillBold(t *testing.T) {
	in := "BenchmarkX/8/a \t 100\t 10.00 ns/op\nBenchmarkX/8/b \t 100\t 20.00 ns/op\n"
	var sb strings.Builder
	if err := run(strings.NewReader(in), &sb); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sb.String(), "**10.00**") {
		t.Errorf("winner not bolded:\n%s", sb.String())
	}
}

// -label and -level place the tables in a larger document. Benchmark names
// are unique only within a package, and this repository has three packages
// with a Backends apiece.
func TestLabelAndLevel(t *testing.T) {
	oldL, oldV := *label, *level
	defer func() { *label, *level = oldL, oldV }()

	in := "BenchmarkBackends/64/scalar \t 100\t 10.00 ns/op\n"

	*label, *level = "", 2
	var plain strings.Builder
	if err := run(strings.NewReader(in), &plain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain.String(), "## Backends\n") {
		t.Errorf("default heading wrong:\n%s", plain.String())
	}

	*label, *level = "rapidhash", 3
	var labelled strings.Builder
	if err := run(strings.NewReader(in), &labelled); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(labelled.String(), "### rapidhash: Backends\n") {
		t.Errorf("labelled heading wrong:\n%s", labelled.String())
	}

	// Out-of-range levels fall back rather than emitting nonsense.
	*level = 99
	var bad strings.Builder
	if err := run(strings.NewReader(in), &bad); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bad.String(), "## rapidhash: Backends\n") {
		t.Errorf("out-of-range level not clamped:\n%s", bad.String())
	}
}
