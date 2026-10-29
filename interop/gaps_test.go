package interop

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type knownGap struct {
	direction, adapter string
	re                 *regexp.Regexp
	reason             string
}

func loadKnownGaps(t *testing.T) []knownGap {
	t.Helper()
	f, err := os.Open("testdata/known-gaps.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var gaps []knownGap
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		col := strings.Split(line, "\t")
		if len(col) != 4 {
			t.Fatalf("known-gaps.tsv: %d columns in %q", len(col), line)
		}
		gaps = append(gaps, knownGap{col[0], col[1], regexp.MustCompile(col[2]), col[3]})
	}
	return gaps
}

// find returns the known gap matching the case, or nil.
func findGap(gaps []knownGap, direction, adapter string, tc testCase) *knownGap {
	for i, g := range gaps {
		if g.direction == direction && (g.adapter == "*" || g.adapter == adapter) && g.re.MatchString(tc.display) {
			return &gaps[i]
		}
	}
	return nil
}

// check compares a result with the expectation of the case. A mismatch
// covered by a known gap is a skip; a known gap that no longer reproduces is
// an error, so the entry is removed.
func check(t *testing.T, gaps []knownGap, direction, adapter string, tc testCase, res *result) {
	t.Helper()
	var problems []string
	if got := res.summary(); got != tc.want {
		problems = append(problems, "summary\n got: "+got+"\nwant: "+tc.want)
	}
	if res.exit != tc.exit {
		problems = append(problems, "exit code "+strconv.Itoa(res.exit)+", want "+strconv.Itoa(tc.exit))
	}
	gap := findGap(gaps, direction, adapter, tc)
	switch {
	case gap != nil && len(problems) > 0:
		t.Skipf("known gap: %s\n%s", gap.reason, strings.Join(problems, "\n"))
	case gap != nil:
		t.Errorf("stale entry in testdata/known-gaps.tsv, the case passes now: %s", gap.reason)
	case len(problems) > 0:
		t.Error(strings.Join(problems, "\n"))
	}
}
