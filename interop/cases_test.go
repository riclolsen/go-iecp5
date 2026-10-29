package interop

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// result is the client result document of the container contract
// (docs/CONTAINER_CONTRACT.md in iec104-interop). The reference clients print
// one; the in-process go-iecp5 client builds the same shape so that one table
// of cases serves both directions.
type result struct {
	Adapter   string `json:"adapter"`
	Operation string `json:"operation"`
	OK        bool   `json:"ok"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Connected        bool `json:"connected"`
	StartdtConfirmed bool `json:"startdtConfirmed"`
	StopdtConfirmed  bool `json:"stopdtConfirmed"`
	Confirmations    []struct {
		Cot      int  `json:"cot"`
		Negative bool `json:"negative"`
	} `json:"confirmations"`
	Terminated bool      `json:"terminated"`
	ASDUs      []asduDoc `json:"asdus"`
	File       *fileDoc  `json:"file"`
	exit       int       // process exit code (reference clients) or derived
}

type asduDoc struct {
	Type          string           `json:"type"`
	TypeID        int              `json:"typeId"`
	Cot           int              `json:"cot"`
	Negative      bool             `json:"negative"`
	CommonAddress int              `json:"commonAddress"`
	Objects       []map[string]any `json:"objects"`
}

type fileDoc struct {
	Ioa      uint32 `json:"ioa"`
	Name     int    `json:"name"`
	Length   int    `json:"length"`
	Received int    `json:"received"`
	Sections int    `json:"sections"`
	Sha256   string `json:"sha256"`
}

// summary renders a result the way tests/cases.tsv expects it:
// ok, error code, confirmations (cause, "n" when negative), termination and
// type/cause of every received ASDU.
func (r *result) summary() string {
	errCode := "-"
	if r.Error != nil {
		errCode = r.Error.Code
	}
	conf := make([]string, 0, len(r.Confirmations))
	for _, c := range r.Confirmations {
		s := strconv.Itoa(c.Cot)
		if c.Negative {
			s += "n"
		}
		conf = append(conf, s)
	}
	asdus := make([]string, 0, len(r.ASDUs))
	for _, a := range r.ASDUs {
		asdus = append(asdus, fmt.Sprintf("%s/%d", a.Type, a.Cot))
	}
	return fmt.Sprintf("ok=%t error=%s conf=%s term=%t asdus=%s",
		r.OK, errCode, strings.Join(conf, ","), r.Terminated, strings.Join(asdus, ","))
}

// testCase is one line of testdata/cases.tsv.
type testCase struct {
	rw      bool     // the case changes the station: it gets a fresh one
	exit    int      // expected exit code of the client
	want    string   // expected summary
	needs   []string // "server:<feature>" / "client:<feature>"
	args    []string // operation and flags
	display string
}

func loadCases(t *testing.T) []testCase {
	t.Helper()
	f, err := os.Open("testdata/cases.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var cases []testCase
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		col := strings.Split(line, "\t")
		if len(col) != 5 {
			t.Fatalf("cases.tsv: %d columns in %q", len(col), line)
		}
		exit, err := strconv.Atoi(col[1])
		if err != nil {
			t.Fatalf("cases.tsv: %v", err)
		}
		tc := testCase{rw: col[0] == "rw", exit: exit, want: col[2], args: strings.Fields(col[4]), display: col[4]}
		if col[3] != "-" {
			tc.needs = strings.Split(col[3], ",")
		}
		cases = append(cases, tc)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return cases
}

// missing returns the first need the pairing lacks, or "".
func (tc testCase) missing(server, client capabilities) string {
	for _, n := range tc.needs {
		role, feature, _ := strings.Cut(n, ":")
		c := server
		if role == "client" {
			c = client
		}
		if !c.has(feature) {
			return n
		}
	}
	return ""
}

// flag returns the value of --name in args, or def.
func flag(args []string, name, def string) string {
	for i, a := range args {
		if a == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return def
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name {
			return true
		}
	}
	return false
}

func flagInt(args []string, name string, def int) int {
	v := flag(args, name, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		panic(fmt.Sprintf("--%s %q: %v", name, v, err))
	}
	return n
}

// qualityFlags is the quality descriptor as the documents print it.
func qualityFlags(q byte) []string {
	flags := []string{}
	for _, f := range []struct {
		name string
		bit  byte
	}{{"OV", 0x01}, {"BL", 0x10}, {"SB", 0x20}, {"NT", 0x40}, {"IV", 0x80}} {
		if q&f.bit != 0 {
			flags = append(flags, f.name)
		}
	}
	return flags
}
