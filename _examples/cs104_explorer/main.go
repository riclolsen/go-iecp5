// Command cs104-explorer is a full-screen terminal browser for one IEC
// 60870-5-104 outstation, driven by the keyboard or the mouse.
//
// It answers the question you actually have when pointing at an unfamiliar
// device: what is this thing reporting, and does it respond?
//
//	cs104-explorer -host 10.0.0.5:2404
//	cs104-explorer -demo
//
// -demo needs no hardware: it runs a full outstation inside the same process
// over a loopback socket — the real APCI state machine, the real ASDU codecs
// and the real file transfer, with no device.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/riclolsen/go-iecp5/asdu"
)

func main() {
	var (
		host      = flag.String("host", "", "outstation address (host:port)")
		demo      = flag.Bool("demo", false, "run a simulated outstation in-process")
		ca        = flag.Uint("ca", 1, "ASDU common address of the outstation")
		orig      = flag.Uint("orig", 0, "originator address (0 when unused)")
		timeout   = flag.Duration("timeout", 30*time.Second, "connect timeout (t0)")
		reconnect = flag.Duration("reconnect", 10*time.Second, "reconnect interval")

		mouse   = flag.Bool("mouse", true, "enable the mouse")
		inline  = flag.Bool("inline", false, "draw inline instead of taking the whole terminal")
		stale   = flag.Duration("stale", 30*time.Second, "fade points not updated for this long; 0 disables")
		history = flag.Int("history", defaultHistory,
			"how many arrivals the event list keeps; 0 keeps everything. One general interrogation of an N object device produces N arrivals")
		outDir = flag.String("file-dir", defaultDownloadDir, "directory completed file transfers are written to")

		direct    = flag.Bool("direct", false, "direct execute instead of select before execute")
		noConfirm = flag.Bool("no-confirm", false, "issue commands without asking first")
		pulse     = flag.String("pulse", "short", "command pulse: none, short, long or persistent")
		verbose   = flag.Bool("v", false, "start with protocol logging on")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "cs104-explorer -host HOST:PORT [flags]\ncs104-explorer -demo\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	lk := defaultLink()
	lk.CommonAddr = uint16(*ca)
	lk.Originator = byte(*orig)
	lk.Timeout, lk.Reconnect = *timeout, *reconnect

	switch {
	case *demo:
		lk.Demo = true
	case *host != "":
		if err := parseAddress(*host, &lk); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	case flag.NArg() == 1:
		// A bare address as the only argument is what people type first.
		if err := parseAddress(flag.Arg(0), &lk); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	default:
		flag.Usage()
		fmt.Fprintln(os.Stderr, "\ngive -host, an address, or -demo")
		os.Exit(2)
	}

	if lk.CommonAddr == 0 {
		fmt.Fprintln(os.Stderr, "common address 0 is not used")
		os.Exit(2)
	}

	qoc, err := parsePulse(*pulse)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	conn := newConnection(lk, *outDir)
	conn.setVerbose(*verbose)

	m := NewModel(conn)
	m.sbo = !*direct
	m.confirm = !*noConfirm
	m.qoc = qoc
	m.mouse = *mouse
	m.altmode = !*inline
	m.staleAge = *stale
	m.history = *history
	if m.history < 0 {
		m.history = 0
	}
	m.files.dir = *outDir

	if err := conn.start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.stop()

	opts := []tea.ProgramOption{}
	if !*inline {
		opts = append(opts, tea.WithAltScreen())
	}
	if *mouse {
		// All-motion reporting is what makes tabs and buttons light up under
		// the pointer; a control surface that does not acknowledge the
		// pointer feels broken.
		opts = append(opts, tea.WithMouseAllMotion())
	}

	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func parsePulse(s string) (asdu.QOCQual, error) {
	switch s {
	case "none", "0":
		return asdu.QOCNoAdditionalDefinition, nil
	case "short", "1":
		return asdu.QOCShortPulseDuration, nil
	case "long", "2":
		return asdu.QOCLongPulseDuration, nil
	case "persistent", "3":
		return asdu.QOCPersistentOutput, nil
	}
	return 0, fmt.Errorf("pulse: %q is not none, short, long or persistent", s)
}
