package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// link is the connection setup: which device, and as whom.
//
// These are the parameters an operator has to get right in front of an
// unfamiliar device and usually cannot, because a common address read off a
// drawing is a guess until something answers. Quitting and restarting the
// tool to try 2 instead of 1 is how ten minutes of commissioning becomes an
// afternoon, so they are editable while it runs and applied by reconnecting.
type link struct {
	// Demo runs an outstation inside this process instead of dialling out.
	Demo bool
	Host string

	// CommonAddr is the ASDU common address of the station being addressed,
	// and Originator the originator address this master signs with.
	CommonAddr uint16
	Originator byte

	Timeout   time.Duration
	Reconnect time.Duration
}

func defaultLink() link {
	return link{
		Host:       "127.0.0.1:2404",
		CommonAddr: 1,
		Timeout:    30 * time.Second,
		Reconnect:  10 * time.Second,
	}
}

// target names the device for the header and the overview.
func (l link) target() string {
	if l.Demo {
		return "demo (in-process outstation)"
	}
	return l.Host
}

// address renders the transport the way the editor accepts it back, so what
// the operator sees in the field is what they could have typed.
func (l link) address() string {
	if l.Demo {
		return "demo"
	}
	return l.Host
}

// sameDevice reports whether two setups point at the same station. Changing a
// timeout leaves the measurements on screen meaningful; changing the address
// or the common address does not, because they then describe a device this
// tool is no longer talking to.
func (l link) sameDevice(o link) bool {
	return l.Demo == o.Demo && l.Host == o.Host && l.CommonAddr == o.CommonAddr
}

// parseAddress reads the one field that covers both transports: "demo", or a
// host and port. One field rather than two because they are alternatives, not
// a combination.
func parseAddress(s string, into *link) error {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return fmt.Errorf("address: give a host:port, or demo")
	case strings.EqualFold(s, "demo"):
		into.Demo, into.Host = true, ""
		return nil
	}
	if !strings.Contains(s, ":") {
		s += ":2404" // the IANA port for IEC 60870-5-104
	}
	if _, _, err := splitHostPort(s); err != nil {
		return err
	}
	into.Demo, into.Host = false, s
	return nil
}

func splitHostPort(s string) (string, int, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("address: %q needs a port", s)
	}
	host, portText := s[:i], s[i+1:]
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("address: %q is not a port", portText)
	}
	return host, port, nil
}

// parseUint reads a decimal or 0x-prefixed number that must fit in bits.
func parseUint(s string, bits int) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("a number is required")
	}
	v, err := strconv.ParseUint(s, 0, bits)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number in [0, %d]", s, uint64(1)<<bits-1)
	}
	return v, nil
}

func parseDurationField(s string, name string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration such as 15s", name, s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return d, nil
}
