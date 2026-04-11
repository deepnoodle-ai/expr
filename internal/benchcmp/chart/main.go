// Chart generates grouped bar charts (SVG) from `go test -bench` output.
//
// Usage:
//
//	go test -run=^$ -bench=. ./... | go run ./chart [out-dir]
//
// Writes bench_run.svg and bench_compile.svg into out-dir (default: cwd).
package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type sample struct {
	lib   string // "DN", "EL", "CEL"
	phase string // "Compile" | "Run"
	name  string // expression case (e.g. "literal")
	ns    float64
}

var benchRe = regexp.MustCompile(`^Benchmark(DN|EL|CEL)(Compile|Run)/([^-]+)-\d+\s+\d+\s+([\d.]+)\s+ns/op`)

func parse(r io.Reader) []sample {
	var out []sample
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		m := benchRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		ns, err := strconv.ParseFloat(m[4], 64)
		if err != nil {
			continue
		}
		out = append(out, sample{lib: m[1], phase: m[2], name: m[3], ns: ns})
	}
	return out
}

var (
	libs      = []string{"DN", "EL", "CEL"}
	libColors = map[string]string{
		"DN":  "#2563eb",
		"EL":  "#16a34a",
		"CEL": "#ea580c",
	}
	libNames = map[string]string{
		"DN":  "deepnoodle/expr",
		"EL":  "expr-lang/expr",
		"CEL": "cel-go",
	}
)

func svg(samples []sample, phase string) string {
	var cases []string
	seen := map[string]bool{}
	data := map[string]map[string]float64{}
	for _, s := range samples {
		if s.phase != phase {
			continue
		}
		if !seen[s.name] {
			seen[s.name] = true
			cases = append(cases, s.name)
		}
		if data[s.name] == nil {
			data[s.name] = map[string]float64{}
		}
		data[s.name][s.lib] = s.ns
	}

	const (
		W, H                   = 960, 520
		mL, mR, mT, mB float64 = 70, 170, 60, 90
	)
	plotW := float64(W) - mL - mR
	plotH := float64(H) - mT - mB

	maxVal := 1.0
	for _, m := range data {
		for _, v := range m {
			if v > maxVal {
				maxVal = v
			}
		}
	}
	decMax := math.Ceil(math.Log10(maxVal))
	if decMax < 1 {
		decMax = 1
	}

	yFor := func(v float64) float64 {
		if v < 1 {
			v = 1
		}
		return mT + plotH*(1-math.Log10(v)/decMax)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" font-family="-apple-system, system-ui, sans-serif" font-size="12">`, W, H)
	fmt.Fprint(&sb, `<rect width="100%" height="100%" fill="white"/>`)

	title := fmt.Sprintf("%s — ns/op, log scale, lower is better", phaseTitle(phase))
	fmt.Fprintf(&sb, `<text x="%.0f" y="30" font-size="16" font-weight="600">%s</text>`, mL, title)

	// Y grid + decade labels
	for i := 0; i <= int(decMax); i++ {
		y := mT + plotH*(1-float64(i)/decMax)
		label := fmt.Sprintf("%s ns", humanDecade(i))
		fmt.Fprintf(&sb, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#e5e7eb" stroke-width="1"/>`, mL, y, mL+plotW, y)
		fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" text-anchor="end" dominant-baseline="middle" fill="#6b7280">%s</text>`, mL-8, y, label)
	}
	// Baseline
	fmt.Fprintf(&sb, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#9ca3af" stroke-width="1"/>`, mL, mT+plotH, mL+plotW, mT+plotH)

	// Bars
	nGroups := len(cases)
	if nGroups == 0 {
		sb.WriteString(`</svg>`)
		return sb.String()
	}
	groupW := plotW / float64(nGroups)
	barW := groupW / float64(len(libs)+1)

	for gi, c := range cases {
		groupLeft := mL + float64(gi)*groupW
		barsWidth := float64(len(libs)) * barW
		barsLeft := groupLeft + (groupW-barsWidth)/2
		for bi, lib := range libs {
			v := data[c][lib]
			if v <= 0 {
				continue
			}
			x := barsLeft + float64(bi)*barW
			y := yFor(v)
			bh := mT + plotH - y
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"><title>%s %s: %s ns/op</title></rect>`,
				x, y, barW-2, bh, libColors[lib], libNames[lib], c, formatNs(v))
		}
		cx := groupLeft + groupW/2
		labelY := mT + plotH + 18
		fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" text-anchor="end" transform="rotate(-30 %.1f %.1f)">%s</text>`,
			cx, labelY, cx, labelY, c)
	}

	// Legend
	lx := mL + plotW + 20
	ly := mT + 10
	for i, lib := range libs {
		y := ly + float64(i)*22
		fmt.Fprintf(&sb, `<rect x="%.0f" y="%.0f" width="14" height="14" fill="%s"/>`, lx, y, libColors[lib])
		fmt.Fprintf(&sb, `<text x="%.0f" y="%.0f">%s</text>`, lx+20, y+11, libNames[lib])
	}

	sb.WriteString(`</svg>`)
	return sb.String()
}

func phaseTitle(phase string) string {
	switch phase {
	case "Run":
		return "Eval (compiled expression, per call)"
	case "Compile":
		return "Compile (per call)"
	}
	return phase
}

func humanDecade(exp int) string {
	v := math.Pow10(exp)
	switch {
	case v >= 1e6:
		return fmt.Sprintf("%gM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%gk", v/1e3)
	default:
		return fmt.Sprintf("%g", v)
	}
}

func formatNs(v float64) string {
	switch {
	case v >= 1000:
		return fmt.Sprintf("%.1fk", v/1000)
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprintf("%.1f", v)
	}
}

func main() {
	samples := parse(os.Stdin)
	if len(samples) == 0 {
		fmt.Fprintln(os.Stderr, "no benchmark lines parsed from stdin")
		os.Exit(1)
	}

	outDir := "."
	if len(os.Args) >= 2 {
		outDir = os.Args[1]
	}

	for _, phase := range []string{"Run", "Compile"} {
		path := fmt.Sprintf("%s/bench_%s.svg", outDir, strings.ToLower(phase))
		if err := os.WriteFile(path, []byte(svg(samples, phase)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "wrote", path)
	}
}
