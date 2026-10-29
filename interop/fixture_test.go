package interop

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// fixture is the part of fixtures/<name>/fixture.json the tests use. The
// expected values come from the image under test (print-fixture), never from
// a copy kept here.
type fixture struct {
	Name    string `json:"name"`
	Station struct {
		CommonAddress uint16 `json:"commonAddress"`
	} `json:"station"`
	Points []struct {
		Ioa       uint32   `json:"ioa"`
		Type      string   `json:"type"`
		Value     number   `json:"value"`
		Quality   []string `json:"quality"`
		Transient bool     `json:"transient"`
		Sequence  byte     `json:"sequence"`
	} `json:"points"`
	Commands []struct {
		Ioa            uint32 `json:"ioa"`
		Type           string `json:"type"`
		Target         uint32 `json:"target"`
		ReportCause    int    `json:"reportCause"`
		SelectRequired bool   `json:"selectRequired"`
	} `json:"commands"`
	Files []struct {
		Ioa         uint32 `json:"ioa"`
		Name        uint16 `json:"name"`
		Size        int    `json:"size"`
		SectionSize int    `json:"sectionSize"`
	} `json:"files"`
}

func loadFixture(t *testing.T, adapter string) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := docker(ctx, "run", "--rm", image(adapter), "print-fixture", "baseline")
	if err != nil {
		t.Fatalf("print-fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal([]byte(out), &f); err != nil {
		t.Fatalf("print-fixture: %v", err)
	}
	return &f
}

// number is a JSON number or boolean, as a float64.
type number float64

func (n *number) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "true":
		*n = 1
	case "false":
		*n = 0
	default:
		var f float64
		if err := json.Unmarshal(b, &f); err != nil {
			return err
		}
		*n = number(f)
	}
	return nil
}

// fileByte is octet i of a fixture file: (i + ioa) mod 251.
func fileByte(ioa uint32, i int) byte { return byte((i + int(ioa)) % 251) }

func fileContent(ioa uint32, size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = fileByte(ioa, i)
	}
	return b
}

// qualityBits maps the fixture's flag names to the quality descriptor octet.
// Integrated totals use CY, CA, IV in the octet's own layout, but the
// comparison is done on flag names, see flagsOf in the tests.
var qdsBit = map[string]byte{"OV": 0x01, "BL": 0x10, "SB": 0x20, "NT": 0x40, "IV": 0x80}

func qdsOf(flags []string) byte {
	var b byte
	for _, f := range flags {
		b |= qdsBit[f]
	}
	return b
}
