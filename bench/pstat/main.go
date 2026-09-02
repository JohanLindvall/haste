// Command pstat runs one benchmark cell under perf stat and reports its
// hardware counters per operation.
//
// It is the arithmetic CLAUDE.md prescribes, done once instead of by hand:
// cycles per op is ns/op times the clock perf measured (cycles over
// task-clock), and every other event is its ratio to cycles times that, so
// the benchmark's calibration ramp -- which perf counts and the benchmark
// does not report -- cancels. Zen 4 has six programmable counters, so the
// events are taken in groups of five plus cycles, one run of the cell per
// group, rather than multiplexed.
//
// Usage:
//
//	go build -o /tmp/b.test . && go run ./pstat -bin /tmp/b.test \
//	    -bench 'BenchmarkCompare64/^64$/^haste-xxh3$' -groups core,front,mem
//
// The bench regexp must select exactly one cell; anchor each element, since
// go test matches the elements of a benchmark name separately and "8"
// matches "128". The event names are Zen 4's; -events takes any list for
// another core.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var groups = map[string][]string{
	"core": {"instructions", "ex_ret_ops", "ex_ret_brn", "ex_ret_brn_tkn", "ex_ret_brn_misp"},
	"front": {"de_src_op_disp.op_cache", "de_src_op_disp.decoder",
		"de_no_dispatch_per_slot.no_ops_from_frontend", "de_no_dispatch_per_slot.backend_stalls", "ex_ret_ucode_ops"},
	"mem": {"ls_dispatch.ld_dispatch", "ls_dispatch.store_dispatch", "ls_stlf", "ls_bad_status2.stli_other",
		"ls_misal_loads.ma64"},
	"tokens": {"de_dis_dispatch_token_stalls1.int_phy_reg_file_rsrc_stall",
		"de_dis_dispatch_token_stalls1.fp_reg_file_rsrc_stall", "de_dis_dispatch_token_stalls1.load_queue_rsrc_stall",
		"de_dis_dispatch_token_stalls1.store_queue_rsrc_stall", "de_dis_dispatch_token_stalls2.retire_token_stall"},
	"fp": {"fp_ops_retired_by_width.all", "fp_ops_retired_by_width.pack_512_uops_retired",
		"fp_ops_retired_by_width.pack_256_uops_retired", "fp_ops_retired_by_width.pack_128_uops_retired", "fp_disp_faults.all"},
}

// short is the column name each event prints under.
var short = map[string]string{
	"instructions": "inst", "ex_ret_ops": "ops", "ex_ret_brn": "brn", "ex_ret_brn_tkn": "tkn", "ex_ret_brn_misp": "misp",
	"de_src_op_disp.op_cache": "opcache", "de_src_op_disp.decoder": "decoder",
	"de_no_dispatch_per_slot.no_ops_from_frontend": "fe_slots", "de_no_dispatch_per_slot.backend_stalls": "be_slots",
	"ex_ret_ucode_ops":        "ucode",
	"ls_dispatch.ld_dispatch": "ld", "ls_dispatch.store_dispatch": "st", "ls_stlf": "stlf",
	"ls_bad_status2.stli_other": "stli", "ls_misal_loads.ma64": "ma64",
	"de_dis_dispatch_token_stalls1.int_phy_reg_file_rsrc_stall": "iprf",
	"de_dis_dispatch_token_stalls1.fp_reg_file_rsrc_stall":      "fprf",
	"de_dis_dispatch_token_stalls1.load_queue_rsrc_stall":       "ldq",
	"de_dis_dispatch_token_stalls1.store_queue_rsrc_stall":      "stq",
	"de_dis_dispatch_token_stalls2.retire_token_stall":          "retire",
	"fp_ops_retired_by_width.all":                               "fpops", "fp_ops_retired_by_width.pack_512_uops_retired": "fp512",
	"fp_ops_retired_by_width.pack_256_uops_retired": "fp256", "fp_ops_retired_by_width.pack_128_uops_retired": "fp128",
	"fp_disp_faults.all": "fpfault",
}

var cellRE = regexp.MustCompile(`(?m)^(\S+?)(?:-\d+)?\s+(\d+)\s+([\d.]+) ns/op`)

func main() {
	bin := flag.String("bin", "", "test binary to run")
	bench := flag.String("bench", "", "-test.bench regexp selecting exactly one cell")
	groupList := flag.String("groups", "core", "comma-separated event groups: core, front, mem, tokens, fp")
	events := flag.String("events", "", "explicit comma-separated events, at most five, instead of -groups")
	benchtime := flag.String("benchtime", "1s", "-test.benchtime for each run")
	core := flag.Int("core", 2, "core to pin the benchmark to")
	flag.Parse()
	if *bin == "" || *bench == "" {
		fmt.Fprintln(os.Stderr, "pstat: -bin and -bench are required")
		os.Exit(2)
	}
	var sets [][]string
	if *events != "" {
		sets = append(sets, strings.Split(*events, ","))
	} else {
		for _, g := range strings.Split(*groupList, ",") {
			evs, ok := groups[g]
			if !ok {
				fmt.Fprintf(os.Stderr, "pstat: unknown group %q\n", g)
				os.Exit(2)
			}
			sets = append(sets, evs)
		}
	}
	var name string
	cols := []string{}
	vals := map[string]float64{}
	var ns, ghz, cyc float64
	for _, evs := range sets {
		n, nsop, clock, cycOp, per, err := run(*bin, *bench, evs, *benchtime, *core)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pstat:", err)
			os.Exit(1)
		}
		name, ns, ghz, cyc = n, nsop, clock, cycOp
		for _, e := range evs {
			c := short[e]
			if c == "" {
				c = e
			}
			cols = append(cols, c)
			vals[c] = per[e]
		}
	}
	fmt.Printf("%s ns=%.2f GHz=%.2f cyc=%.1f", name, ns, ghz, cyc)
	for _, c := range cols {
		fmt.Printf(" %s=%.2f", c, vals[c])
	}
	fmt.Println()
}

// run times the cell once under perf stat with cycles, task-clock and evs,
// and returns per-op figures.
func run(bin, bench string, evs []string, benchtime string, core int) (name string, ns, ghz, cyc float64, per map[string]float64, err error) {
	args := []string{"stat", "-x", ",", "-e", "task-clock,cycles," + strings.Join(evs, ","), "--",
		"taskset", "-c", strconv.Itoa(core), bin, "-test.run=^$", "-test.bench=" + bench,
		"-test.benchtime=" + benchtime, "-test.count=1"}
	cmd := exec.Command("perf", args...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if e := cmd.Run(); e != nil {
		return "", 0, 0, 0, nil, fmt.Errorf("%v\n%s%s", e, out.String(), errb.String())
	}
	cells := cellRE.FindAllStringSubmatch(out.String(), -1)
	if len(cells) != 1 {
		return "", 0, 0, 0, nil, fmt.Errorf("%d benchmark cells matched %q; anchor the pattern to select one:\n%s",
			len(cells), bench, out.String())
	}
	name = cells[0][1]
	ns, _ = strconv.ParseFloat(cells[0][3], 64)
	counts := map[string]float64{}
	var clockMS float64
	for _, line := range strings.Split(errb.String(), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 3 {
			continue
		}
		v, e := strconv.ParseFloat(f[0], 64)
		if e != nil {
			continue
		}
		ev := strings.TrimSuffix(f[2], ":u")
		if ev == "task-clock" {
			clockMS = v
		} else {
			counts[ev] = v
		}
	}
	cycles := counts["cycles"]
	if cycles == 0 || clockMS == 0 {
		return "", 0, 0, 0, nil, fmt.Errorf("perf reported no cycles or task-clock:\n%s", errb.String())
	}
	ghz = cycles / (clockMS * 1e6)
	cyc = ns * ghz
	per = map[string]float64{}
	for _, e := range evs {
		per[e] = counts[e] / cycles * cyc
	}
	return name, ns, ghz, cyc, per, nil
}
