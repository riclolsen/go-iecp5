package interop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runRefClient runs one operation of the reference client of the adapter
// against a station on the host and returns its result document.
func runRefClient(t *testing.T, adapter string, port int, args []string) *result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	full := append([]string{"run", "--rm", "--add-host=host.docker.internal:host-gateway", image(adapter),
		"client", args[0], "--host", "host.docker.internal", "--port", fmt.Sprint(port)}, args[1:]...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	exit := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("docker: %v", err)
		}
		exit = ee.ExitCode()
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	res := &result{exit: exit}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), res); err != nil {
		t.Fatalf("client result (exit %d): %v\nstdout: %s\nstderr: %s", exit, err, out.String(), errb.String())
	}
	if t.Failed() || exit == 2 {
		t.Logf("client stderr:\n%s", errb.String())
	}
	return res
}

// TestOutstation serves the baseline fixture with go-iecp5's cs104.Server and
// drives it with each reference client: every case of testdata/cases.tsv.
func TestOutstation(t *testing.T) {
	cases := loadCases(t)
	gaps := loadKnownGaps(t)
	for _, adapter := range adapters() {
		t.Run(adapter, func(t *testing.T) {
			t.Parallel()
			requireImage(t, adapter)
			caps := getCapabilities(t, adapter)
			if !caps.Roles.Client {
				t.Skip("adapter has no client role")
			}
			fx := loadFixture(t, adapter)
			shared := startStation(t, newStation(fx))
			for _, tc := range cases {
				t.Run(tc.display, func(t *testing.T) {
					if m := tc.missing(selfCapabilities, caps); m != "" {
						t.Skipf("needs %s", m)
					}
					if !caps.canDo(tc.args[0]) {
						t.Skipf("client has no operation %s", tc.args[0])
					}
					port := shared
					if tc.rw {
						port = startStation(t, newStation(fx))
					}
					check(t, gaps, "outstation", adapter, tc, runRefClient(t, adapter, port, tc.args))
				})
			}
		})
	}
}

// TestOutstationInterrogationValues checks the values and quality of every
// point the reference client received from go-iecp5, against the fixture.
func TestOutstationInterrogationValues(t *testing.T) {
	for _, adapter := range adapters() {
		t.Run(adapter, func(t *testing.T) {
			t.Parallel()
			requireImage(t, adapter)
			caps := getCapabilities(t, adapter)
			if !caps.Roles.Client || !caps.canDo("interrogate") {
				t.Skip("adapter cannot interrogate")
			}
			fx := loadFixture(t, adapter)
			port := startStation(t, newStation(fx))
			res := runRefClient(t, adapter, port, []string{"interrogate"})
			if !res.OK {
				t.Fatalf("interrogate: %s", res.summary())
			}
			checkPoints(t, fx, res)
		})
	}
}

// TestOutstationFileSHA256 compares the file the reference client downloaded
// from go-iecp5 with the fixture's deterministic content.
func TestOutstationFileSHA256(t *testing.T) {
	for _, adapter := range adapters() {
		t.Run(adapter, func(t *testing.T) {
			t.Parallel()
			requireImage(t, adapter)
			caps := getCapabilities(t, adapter)
			if !caps.Roles.Client || !caps.has("fileClient") {
				t.Skip("adapter has no file client")
			}
			fx := loadFixture(t, adapter)
			port := startStation(t, newStation(fx))
			gaps := loadKnownGaps(t)
			for _, f := range fx.Files {
				tc := testCase{display: fmt.Sprintf("file-get --ioa %d", f.Ioa)}
				if g := findGap(gaps, "outstation", adapter, tc); g != nil {
					t.Logf("file %d: known gap: %s", f.Ioa, g.reason)
					continue
				}
				res := runRefClient(t, adapter, port, []string{"file-get", "--ioa", fmt.Sprint(f.Ioa), "--name", fmt.Sprint(f.Name)})
				if !res.OK || res.File == nil {
					t.Errorf("file %d: %s", f.Ioa, res.summary())
					continue
				}
				if want := sha256Hex(fileContent(f.Ioa, f.Size)); res.File.Sha256 != want || res.File.Received != f.Size {
					t.Errorf("file %d: received %d octets sha256 %s, want %d octets %s", f.Ioa, res.File.Received, res.File.Sha256, f.Size, want)
				}
			}
		})
	}
}
