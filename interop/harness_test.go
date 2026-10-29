package interop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Interoperability tests against the reference implementations of
// github.com/otfabric/iec104-interop (lib60870, OpenMUC j60870, wendy512 and
// cB-Sigma), run as Docker containers. See README.md in this directory.
//
// The suite is opt-in because it needs Docker and the images:
//
//	IEC104_INTEROP=1 go test ./interop/ -v
//
// IEC104_INTEROP_TAG selects the image tag (default defaultTag),
// IEC104_INTEROP_ADAPTERS narrows the adapters (comma separated).

const (
	defaultTag = "v0.3.1"
	imageBase  = "ghcr.io/otfabric/iec104-interop-"
	readyFile  = "/run/iec104-interop/ready"
)

var allAdapters = []string{"lib60870", "openmuc", "wendy512", "cbsigma"}

func tag() string {
	if v := os.Getenv("IEC104_INTEROP_TAG"); v != "" {
		return v
	}
	return defaultTag
}

func image(adapter string) string { return imageBase + adapter + ":" + tag() }

func adapters() []string {
	if v := os.Getenv("IEC104_INTEROP_ADAPTERS"); v != "" {
		return strings.Split(v, ",")
	}
	return allAdapters
}

// docker runs the docker CLI and returns its stdout. stderr is attached to
// the error.
func docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		err = fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), err
}

// requireImage skips the test unless the interop suite is enabled and the
// image of the adapter is available (it is pulled when missing).
func requireImage(t *testing.T, adapter string) {
	t.Helper()
	if os.Getenv("IEC104_INTEROP") == "" {
		t.Skip("set IEC104_INTEROP=1 to run the interoperability tests (needs Docker)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := docker(ctx, "image", "inspect", image(adapter)); err == nil {
		return
	}
	if _, err := docker(ctx, "pull", "-q", image(adapter)); err != nil {
		t.Skipf("image %s unavailable: %v", image(adapter), err)
	}
}

// capabilities is the subset of print-capabilities the tests use.
type capabilities struct {
	Roles struct {
		Server bool `json:"server"`
		Client bool `json:"client"`
	} `json:"roles"`
	Features         map[string]bool `json:"features"`
	ClientOperations []string        `json:"clientOperations"`
}

func (c capabilities) has(feature string) bool { return c.Features[feature] }

func (c capabilities) canDo(op string) bool {
	for _, o := range c.ClientOperations {
		if o == op {
			return true
		}
	}
	return false
}

func getCapabilities(t *testing.T, adapter string) capabilities {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := docker(ctx, "run", "--rm", image(adapter), "print-capabilities")
	if err != nil {
		t.Fatalf("print-capabilities: %v", err)
	}
	var c capabilities
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("print-capabilities: %v\n%s", err, out)
	}
	return c
}

// refServer is a reference station running in a container.
type refServer struct {
	name string
	Addr string // 127.0.0.1:port on the host
}

// startRefServer starts the reference station of the adapter with the baseline
// fixture and waits for its readiness file (never for a delay).
func startRefServer(t *testing.T, adapter string, extra ...string) *refServer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := append([]string{"run", "-d", "--rm", "-p", "127.0.0.1::2404", image(adapter), "server"}, extra...)
	id, err := docker(ctx, args...)
	if err != nil {
		t.Fatalf("start %s server: %v", adapter, err)
	}
	s := &refServer{name: strings.TrimSpace(id)}
	t.Cleanup(func() {
		if t.Failed() {
			if logs, err := docker(context.Background(), "logs", "--tail", "40", s.name); err == nil {
				t.Logf("%s server events (tail):\n%s", adapter, logs)
			}
		}
		_, _ = docker(context.Background(), "rm", "-f", s.name)
	})

	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := docker(ctx, "exec", s.name, "test", "-f", readyFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s server never became ready", adapter)
		}
		time.Sleep(100 * time.Millisecond)
	}
	port, err := docker(ctx, "port", s.name, "2404/tcp")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(port, "\n") {
		if strings.HasPrefix(l, "127.0.0.1:") {
			s.Addr = strings.TrimSpace(l)
		}
	}
	if s.Addr == "" {
		t.Fatalf("no loopback port mapping in %q", port)
	}
	return s
}

// serverEvents returns the JSON Lines the reference server printed so far.
func (s *refServer) events(t *testing.T) []map[string]any {
	t.Helper()
	out, err := docker(context.Background(), "logs", s.name)
	if err != nil {
		t.Fatal(err)
	}
	var evs []map[string]any
	for _, l := range strings.Split(out, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			evs = append(evs, m)
		}
	}
	return evs
}
