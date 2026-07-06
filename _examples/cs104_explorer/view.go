package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	styTitle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	styOK          = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	styWarn        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	styBad         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	styDim         = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styTabActive   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("205")).Padding(0, 1)
	styTabInactive = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 1)
	styBorder      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	styFooter      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styField       = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styFieldActive = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
)

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "starting IEC 60870-5-104 Explorer..."
	}
	inner := m.width - 2
	if inner < 20 {
		inner = 20
	}
	bodyH := m.height - 6
	if bodyH < 3 {
		bodyH = 3
	}

	// header
	var status string
	switch {
	case m.connected && m.active:
		status = styOK.Render("● active")
	case m.connected:
		status = styWarn.Render("● connected (STOPDT)")
	default:
		status = styBad.Render("● disconnected")
	}
	line1 := styTitle.Render("IEC 60870-5-104 Explorer") + "   " + status
	verbose := "off"
	if m.verbose {
		verbose = "on"
	}
	line2 := styDim.Render(fmt.Sprintf(
		"server %s  •  common addr %d  •  params 104-wide (COT2/CA2/IOA3)  •  protocol log %s",
		m.addr, m.ca, verbose))

	// body content
	var content string
	switch {
	case m.focus == focusAddr:
		content = m.renderConnEdit()
	case m.tab == tabPoints:
		content = m.table.View()
	case m.tab == tabLog:
		content = m.logView.View()
	case m.tab == tabSend:
		content = m.renderForm()
	}

	body := styBorder.Width(inner).Height(bodyH).Render(content)
	footer := styFooter.Render(m.footerHint())

	return lipgloss.JoinVertical(lipgloss.Left, line1, line2, m.renderTabs(), body, footer)
}

func (m model) renderTabs() string {
	names := []string{"1 Points", "2 Log", "3 Send Command"}
	parts := make([]string, len(names))
	for i, n := range names {
		if i == m.tab {
			parts[i] = styTabActive.Render(n)
		} else {
			parts[i] = styTabInactive.Render(n)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func (m model) renderForm() string {
	kind := cmdKinds[m.formKind]
	mode := "Execute"
	if m.formSelect {
		mode = "Select"
	}
	label := func(i int, s string) string {
		if m.focus == focusForm && m.formField == i {
			return styFieldActive.Render("> " + s)
		}
		return styField.Render("  " + s)
	}
	lines := []string{
		label(0, "Command  : ‹ "+kind.name+" ›"),
		label(1, "IOA      : "+m.inIOA.View()),
		label(2, "Value    : "+m.inVal.View()),
		label(3, "Mode     : ‹ "+mode+" ›"),
		label(4, "Qualifier: "+m.inQual.View()),
		"",
		styDim.Render("value — single/double: on|off  •  step: up|down  •  setpoint: number  •  read: (ignored)"),
		styDim.Render(fmt.Sprintf("sends to common address %d (change with 'e')", m.ca)),
	}
	if m.focus != focusForm {
		lines = append(lines, "", styWarn.Render("press 'i' to edit and send"))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderConnEdit() string {
	label := func(i int, s string) string {
		if m.connField == i {
			return styFieldActive.Render("> " + s)
		}
		return styField.Render("  " + s)
	}
	return strings.Join([]string{
		styTitle.Render("Connection settings"),
		"",
		label(0, "Server address : "+m.connAddr.View()),
		label(1, "Common address : "+m.connCA.View()),
		"",
		styDim.Render("enter: apply   •   tab: switch field   •   esc: cancel"),
		styDim.Render("then press 'c' to connect"),
	}, "\n")
}

func (m model) footerHint() string {
	switch m.focus {
	case focusAddr:
		return styFooter.Render("enter apply • tab next field • esc cancel")
	case focusForm:
		return styFooter.Render("↑/↓ or tab: field • ←/→: change option • enter: send • esc: back")
	}
	hint := "1/2/3 tabs • c connect • x disconnect • e edit target • g GI • C counter • y clock • t test • z reset • s/S startdt/stopdt • v log • q quit"
	if m.tab == tabSend {
		hint = "i: edit & send command • " + hint
	}
	return styFooter.Render(hint)
}
