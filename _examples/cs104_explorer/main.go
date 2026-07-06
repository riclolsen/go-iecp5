// Command cs104_explorer is an interactive terminal IEC 60870-5-104 master
// (controlling station). It connects to IEC 104 servers, issues requests
// (general/counter interrogation, clock sync, test, reset), sends control
// commands, and displays received monitored points and a protocol log.
//
// Usage:
//
//	go run .                 # start; edit target and connect in the UI
//	go run . 10.0.0.5:2404   # optional: preset the server address
//
// Keys are shown in the footer of the running program.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	b := &bridge{}
	m := initialModel(b)
	if len(os.Args) > 1 {
		m.addr = os.Args[1]
		m.connAddr.SetValue(os.Args[1])
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	b.setProgram(p)

	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	// Ensure the background client is stopped on exit.
	if fm, ok := final.(model); ok && fm.client != nil {
		_ = fm.client.Close()
	}
}
