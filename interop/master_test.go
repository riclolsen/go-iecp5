package interop

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"testing"
)

// TestMaster runs go-iecp5's cs104.Client against each reference station:
// every case of testdata/cases.tsv, against the baseline fixture.
func TestMaster(t *testing.T) {
	cases := loadCases(t)
	gaps := loadKnownGaps(t)
	for _, adapter := range adapters() {
		t.Run(adapter, func(t *testing.T) {
			t.Parallel()
			requireImage(t, adapter)
			caps := getCapabilities(t, adapter)
			if !caps.Roles.Server {
				t.Skip("adapter has no server role")
			}
			shared := startRefServer(t, adapter)
			for _, tc := range cases {
				t.Run(tc.display, func(t *testing.T) {
					if m := tc.missing(caps, selfCapabilities); m != "" {
						t.Skipf("needs %s", m)
					}
					srv := shared
					if tc.rw {
						srv = startRefServer(t, adapter)
					}
					res := runInProcessClient(t, srv.Addr, tc.args)
					check(t, gaps, "master", adapter, tc, res)
				})
			}
		})
	}
}

// TestMasterInterrogationValues checks the values and quality of every point
// the reference station sends to go-iecp5, against the fixture of its image.
func TestMasterInterrogationValues(t *testing.T) {
	for _, adapter := range adapters() {
		t.Run(adapter, func(t *testing.T) {
			t.Parallel()
			requireImage(t, adapter)
			caps := getCapabilities(t, adapter)
			if !caps.Roles.Server {
				t.Skip("adapter has no server role")
			}
			fx := loadFixture(t, adapter)
			srv := startRefServer(t, adapter)
			res := runInProcessClient(t, srv.Addr, []string{"interrogate"})
			if !res.OK {
				t.Fatalf("interrogate: %s", res.summary())
			}
			checkPoints(t, fx, res)
		})
	}
}

// checkPoints compares every non-counter point of the fixture with the
// objects in the interrogation answer.
func checkPoints(t *testing.T, fx *fixture, res *result) {
	t.Helper()
	got := map[uint32]map[string]any{}
	for _, a := range res.ASDUs {
		if a.Cot != 20 {
			continue
		}
		for _, o := range a.Objects {
			got[uint32(o["ioa"].(float64))] = o
		}
	}
	for _, p := range fx.Points {
		if p.Type == "M_IT_NA_1" {
			continue
		}
		o, ok := got[p.Ioa]
		if !ok {
			t.Errorf("ioa %d (%s) missing from interrogation", p.Ioa, p.Type)
			continue
		}
		if !sameValue(o["value"], float64(p.Value)) {
			t.Errorf("ioa %d (%s): value %v, want %v", p.Ioa, p.Type, o["value"], p.Value)
		}
		if g, w := flagsString(o["quality"]), flagsString(sliceAny(p.Quality)); g != w {
			t.Errorf("ioa %d (%s): quality %q, want %q", p.Ioa, p.Type, g, w)
		}
	}
	if len(got) != len(fx.Points)-countType(fx, "M_IT_NA_1") {
		t.Errorf("interrogation returned %d points, fixture has %d non-counter points",
			len(got), len(fx.Points)-countType(fx, "M_IT_NA_1"))
	}
}

func countType(fx *fixture, typ string) (n int) {
	for _, p := range fx.Points {
		if p.Type == typ {
			n++
		}
	}
	return
}

func sliceAny(s []string) []any { return toAny(s) }

func flagsString(v any) string {
	s := ""
	if l, ok := v.([]any); ok {
		for _, f := range l {
			s += f.(string) + ","
		}
	}
	return s
}

func sameValue(got any, want float64) bool {
	switch v := got.(type) {
	case bool:
		return v == (want != 0)
	case float64:
		return math.Abs(v-want) < 1e-6
	}
	return false
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
