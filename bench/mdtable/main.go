// Command mdtable turns `go test -bench` output into markdown tables, one
// per benchmark, sizes down the side and implementations across the top.
//
//	go test -run xxx -bench Compare -count 5 . | go run ./mdtable
//
// Repetitions from -count collapse to the median, and the best cell of each
// row is bold. Lines that are not benchmark results pass through unseen, so
// the tool can be fed a whole log.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// result is one benchmark line, split into the table coordinates it lands
// at: which table (group), which row (size), which column (impl).
type result struct {
	group, size, impl string
	ns                float64
}

// The -N suffix is GOMAXPROCS, and Go omits it when it is 1 -- which is
// exactly what `go test -cpu 1` produces, and what the bench workflow asks
// for. Requiring it here meant the workflow's own flag guaranteed no line
// ever matched, on every runner at once.
var line = regexp.MustCompile(`^Benchmark(\S+?)(?:-\d+)?\s+\d+\s+([0-9.]+) ns/op`)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mdtable:", err)
		os.Exit(1)
	}
}

// run is main with its ends exposed, so the parsing and the table can be
// tested without a process.
func run(in io.Reader, out io.Writer) error {
	var results []result
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		m := line.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		ns, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		results = append(results, split(m[1], ns))
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(results) == 0 {
		return errors.New("no benchmark lines on stdin")
	}
	emit(out, results)
	return nil
}

// split maps a benchmark name onto table coordinates. The shapes in this
// repository are Group/size/impl, Group/impl, and Group alone; the numeric
// path element, wherever it sits, is the size.
func split(name string, ns float64) result {
	parts := strings.Split(name, "/")
	r := result{group: parts[0], ns: ns}
	var rest []string
	for _, p := range parts[1:] {
		if _, err := strconv.Atoi(p); err == nil && r.size == "" {
			r.size = p
			continue
		}
		rest = append(rest, p)
	}
	r.impl = strings.Join(rest, "/")
	if r.impl == "" {
		r.impl = "ns/op"
	}
	return r
}

func emit(w io.Writer, results []result) {
	// Group and column order is order of appearance; rows sort numerically.
	var groups []string
	cols := map[string][]string{}
	cells := map[string]map[string]map[string][]float64{} // group -> size -> impl -> reps
	for _, r := range results {
		g := cells[r.group]
		if g == nil {
			g = map[string]map[string][]float64{}
			cells[r.group] = g
			groups = append(groups, r.group)
		}
		row := g[r.size]
		if row == nil {
			row = map[string][]float64{}
			g[r.size] = row
		}
		if _, seen := row[r.impl]; !seen && !contains(cols[r.group], r.impl) {
			cols[r.group] = append(cols[r.group], r.impl)
		}
		row[r.impl] = append(row[r.impl], r.ns)
	}

	for gi, g := range groups {
		if gi > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "## %s\n\n", g)
		fmt.Fprintln(w, "ns/op, median of repetitions, lower is better.")
		fmt.Fprintln(w)
		impls := cols[g]
		fmt.Fprintf(w, "| size | %s |\n", strings.Join(impls, " | "))
		fmt.Fprintf(w, "|---:|%s\n", strings.Repeat("---:|", len(impls)))

		sizes := make([]string, 0, len(cells[g]))
		for s := range cells[g] {
			sizes = append(sizes, s)
		}
		sort.Slice(sizes, func(i, j int) bool {
			a, _ := strconv.Atoi(sizes[i])
			b, _ := strconv.Atoi(sizes[j])
			return a < b
		})

		for _, s := range sizes {
			row := cells[g][s]
			best := 0.0
			for _, reps := range row {
				if m := median(reps); best == 0 || m < best {
					best = m
				}
			}
			label := s
			if label == "" {
				label = "-"
			}
			out := []string{label}
			for _, impl := range impls {
				reps, ok := row[impl]
				if !ok {
					out = append(out, "")
					continue
				}
				m := median(reps)
				cell := format(m)
				if m == best {
					cell = "**" + cell + "**"
				}
				out = append(out, cell)
			}
			fmt.Fprintf(w, "| %s |\n", strings.Join(out, " | "))
		}
	}
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

// format keeps enough digits to compare neighbours and no more: sub-100ns
// numbers keep two decimals, everything larger rounds to integers.
func format(ns float64) string {
	switch {
	case ns < 100:
		return strconv.FormatFloat(ns, 'f', 2, 64)
	case ns < 1000:
		return strconv.FormatFloat(ns, 'f', 1, 64)
	default:
		return strconv.FormatFloat(ns, 'f', 0, 64)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
