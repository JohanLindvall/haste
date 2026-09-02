// Command pstat runs one benchmark cell under perf stat and reports its
// hardware counters per operation.
//
// It is the arithmetic CLAUDE.md prescribes, done once instead of by hand:
// cycles per op is ns/op times the clock perf measured (cycles over
// task-clock), and every other event is its ratio to cycles times that, so
// the benchmark's calibration ramp -- which perf counts and the benchmark
// does not report -- cancels. Zen 4 has six programmable counters and the
// Golden Cove line eight, so the events are taken in groups of five plus
// cycles, one run of the cell per group, rather than multiplexed.
//
// Usage:
//
//	go build -o /tmp/b.test . && go run ./pstat -bin /tmp/b.test \
//	    -bench 'BenchmarkCompare64/^64$/^haste-xxh3$' -groups core,front,mem
//
// The bench regexp must select exactly one cell; anchor each element, since
// go test matches the elements of a benchmark name separately and "8"
// matches "128". Two event tables are built in, Zen 4's and the Golden Cove
// line's (Redwood Cove included); the vendor in /proc/cpuinfo picks one and
// -cpu overrides it. -events takes any list for another core. On a hybrid
// Intel part every event is asked of the P-core PMU alone, which is where
// -core pins the cell, so the E-core's copy of each counter does not shadow
// it. The Intel topdown group prints its four metrics as a share of slots
// rather than per operation.
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

// table is one core's event groups and the column each event prints under.
type table struct {
	groups map[string][]string
	short  map[string]string
}

var tables = map[string]table{
	"amd": {
		groups: map[string][]string{
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
		},
		short: map[string]string{
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
		},
	},
	"intel": {
		groups: map[string][]string{
			"core":    {"instructions", "uops_issued.any", "uops_retired.slots", "br_inst_retired.near_taken", "br_misp_retired.all_branches"},
			"topdown": {"slots", "topdown-retiring", "topdown-bad-spec", "topdown-fe-bound", "topdown-be-bound"},
			"front":   {"idq.dsb_uops", "idq.mite_uops", "idq.ms_uops", "idq_bubbles.core", "dsb2mite_switches.penalty_cycles"},
			// Four, not five: mem_inst_retired and mem_load_retired are
			// confined to the first four counters, and a fifth event beside
			// them is multiplexed rather than refused.
			"mem": {"mem_inst_retired.all_loads", "mem_inst_retired.all_stores", "ld_blocks.store_forward",
				"mem_load_retired.l1_miss"},
			"ports": {"uops_dispatched.port_0", "uops_dispatched.port_1", "uops_dispatched.port_5_11", "uops_dispatched.port_6",
				"uops_dispatched.port_2_3_10"},
			"exec": {"uops_executed.thread", "exe_activity.exe_bound_0_ports", "exe_activity.bound_on_loads", "resource_stalls.sb",
				"ld_blocks.no_sr"},
		},
		short: map[string]string{
			"instructions": "inst", "uops_issued.any": "uops", "uops_retired.slots": "rslots",
			"br_inst_retired.near_taken": "tkn", "br_misp_retired.all_branches": "misp",
			"slots": "slots", "topdown-retiring": "retiring", "topdown-bad-spec": "badspec",
			"topdown-fe-bound": "febound", "topdown-be-bound": "bebound",
			"idq.dsb_uops": "dsb", "idq.mite_uops": "mite", "idq.ms_uops": "ms", "idq_bubbles.core": "febub",
			"dsb2mite_switches.penalty_cycles": "d2m",
			"mem_inst_retired.all_loads":       "ld", "mem_inst_retired.all_stores": "st", "ld_blocks.store_forward": "stfwd",
			"ld_blocks.no_sr": "nosr", "mem_load_retired.l1_miss": "l1miss",
			"uops_dispatched.port_0": "p0", "uops_dispatched.port_1": "p1", "uops_dispatched.port_5_11": "p5_11",
			"uops_dispatched.port_6": "p6", "uops_dispatched.port_2_3_10": "p2_3_10",
			"uops_executed.thread": "exec", "exe_activity.exe_bound_0_ports": "0ports", "exe_activity.bound_on_loads": "ldbound",
			"resource_stalls.sb": "sbfull",
		},
	},
}

var cellRE = regexp.MustCompile(`(?m)^(\S+?)(?:-\d+)?\s+(\d+)\s+([\d.]+) ns/op`)

func main() {
	bin := flag.String("bin", "", "test binary to run")
	bench := flag.String("bench", "", "-test.bench regexp selecting exactly one cell")
	groupList := flag.String("groups", "core", "comma-separated event groups; amd: core, front, mem, tokens, fp; intel: core, topdown, front, mem, ports, exec")
	events := flag.String("events", "", "explicit comma-separated events, at most five, instead of -groups")
	benchtime := flag.String("benchtime", "1s", "-test.benchtime for each run")
	core := flag.Int("core", 2, "core to pin the benchmark to")
	cpu := flag.String("cpu", "", "event table: amd or intel (default: the vendor in /proc/cpuinfo)")
	flag.Parse()
	if *bin == "" || *bench == "" {
		fmt.Fprintln(os.Stderr, "pstat: -bin and -bench are required")
		os.Exit(2)
	}
	if *cpu == "" {
		*cpu = vendor()
	}
	tab, ok := tables[*cpu]
	if !ok {
		fmt.Fprintf(os.Stderr, "pstat: no event table for %q\n", *cpu)
		os.Exit(2)
	}
	var sets [][]string
	if *events != "" {
		sets = append(sets, strings.Split(*events, ","))
	} else {
		for _, g := range strings.Split(*groupList, ",") {
			evs, ok := tab.groups[g]
			if !ok {
				fmt.Fprintf(os.Stderr, "pstat: unknown group %q for %s\n", g, *cpu)
				os.Exit(2)
			}
			sets = append(sets, evs)
		}
	}
	// pmu is the PMU every event is asked of. On a hybrid Intel part that
	// is the P-core's, so that the E-core's copy of each counter, which
	// perf would otherwise report under the same name, does not shadow it.
	pmu := ""
	if _, err := os.Stat("/sys/devices/cpu_core"); err == nil {
		pmu = "cpu_core"
	}
	var name string
	cols := []string{}
	vals := map[string]float64{}
	pct := map[string]bool{}
	var ns, ghz, cyc float64
	for _, evs := range sets {
		n, nsop, clock, cycOp, per, err := run(*bin, *bench, evs, *benchtime, *core, pmu)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pstat:", err)
			os.Exit(1)
		}
		name, ns, ghz, cyc = n, nsop, clock, cycOp
		for _, e := range evs {
			c := tab.short[e]
			if c == "" {
				c = e
			}
			cols = append(cols, c)
			vals[c] = per[e]
			pct[c] = strings.HasPrefix(e, "topdown-")
		}
	}
	fmt.Printf("%s ns=%.2f GHz=%.2f cyc=%.1f", name, ns, ghz, cyc)
	for _, c := range cols {
		if pct[c] {
			fmt.Printf(" %s=%.1f%%", c, vals[c])
		} else {
			fmt.Printf(" %s=%.2f", c, vals[c])
		}
	}
	fmt.Println()
}

// vendor reads the CPU vendor from /proc/cpuinfo and names its event table.
func vendor() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err == nil {
		s := string(b)
		switch {
		case strings.Contains(s, "GenuineIntel"):
			return "intel"
		case strings.Contains(s, "AuthenticAMD"):
			return "amd"
		}
	}
	return "amd"
}

// run times the cell once under perf stat with cycles, task-clock and evs,
// and returns per-op figures. Events named topdown-* come back as a
// percentage of slots instead, which is what they are.
func run(bin, bench string, evs []string, benchtime string, core int, pmu string) (name string, ns, ghz, cyc float64, per map[string]float64, err error) {
	qual := func(e string) string {
		if pmu == "" {
			return e
		}
		return pmu + "/" + e + "/"
	}
	list := []string{"task-clock", qual("cycles")}
	for _, e := range evs {
		list = append(list, qual(e))
	}
	args := []string{"stat", "-x", ",", "-e", strings.Join(list, ","), "--",
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
		if len(f) < 5 {
			continue
		}
		v, e := strconv.ParseFloat(f[0], 64)
		if e != nil {
			continue
		}
		ev := strings.TrimSuffix(f[2], ":u")
		// A hybrid part reports pmu/event/; keep the PMU that was asked
		// for and drop the other's copy.
		if i := strings.IndexByte(ev, '/'); i >= 0 {
			if ev[:i] != pmu {
				continue
			}
			ev = strings.TrimSuffix(ev[i+1:], "/")
		}
		if ev == "task-clock" {
			clockMS = v
			continue
		}
		if on, e := strconv.ParseFloat(f[4], 64); e == nil && on < 90 && ev != "cycles" {
			fmt.Fprintf(os.Stderr, "pstat: %s was counted %.0f%% of the time; too many events for the core's counters?\n", ev, on)
		}
		counts[ev] = v
	}
	cycles := counts["cycles"]
	if cycles == 0 || clockMS == 0 {
		return "", 0, 0, 0, nil, fmt.Errorf("perf reported no cycles or task-clock:\n%s", errb.String())
	}
	ghz = cycles / (clockMS * 1e6)
	cyc = ns * ghz
	per = map[string]float64{}
	for _, e := range evs {
		if strings.HasPrefix(e, "topdown-") && counts["slots"] > 0 {
			per[e] = counts[e] / counts["slots"] * 100
			continue
		}
		per[e] = counts[e] / cycles * cyc
	}
	return name, ns, ghz, cyc, per, nil
}
