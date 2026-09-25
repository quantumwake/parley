package tui

// center.go — the command center: knobs, not a window.
//
// Owner, 2026-09-25: "the parley tui was suppose to be that.. not yet
// another console or viewer.. I don't want another parley viewer. so to
// say, need more of a command center for parley controls and nobs, like
// the participant screens and management, so forth. whos following and
// the open readers, and writers, so forth. it needs to be cool and retro."
//
// So the first screen is SEATS — who is on this machine, what each speaks
// under, what each is doing — and the second is CONTROLS, where the knobs
// actually turn: the handle, the optional paths, the listener. The stream
// stays one keypress away for context and is not where the work happens.
//
// Everything drawn here is read from disk, so a keypress never waits on
// the network. What is NOT knowable locally — other people's readers and
// writers across the tenant — is absent rather than faked; it lives in
// statefs.ai's presence surface, not in parley's state.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/quantumwake/parley/pkg/plugin"
)

// The phosphor palette: amber for what is live, green for what is ready,
// dim for what is off. Colour says state, never decoration.
var (
	amber  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	bright = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Bold(true)
	rule   = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	invert = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("214")).Bold(true)
)

// stateDot is the one-glyph reading of what a seat is doing.
func stateDot(s plugin.Seat) string {
	switch {
	case s.Unreachable:
		return dim.Render("○")
	case s.State == "working", s.State == "thinking":
		return amber.Render("●")
	case s.Listening:
		return green.Render("●")
	case s.State == "listening":
		return dim.Render("◐")
	}

	return dim.Render("·")
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "—"
	}

	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}

	return fmt.Sprintf("%dh", int(d.Hours()))
}

// cell pads to a width and THEN styles it: styling first counts the
// escape codes as characters and every column downstream goes ragged.
// The padding counts columns rather than bytes, because a dash or a dot
// in a status is one column and three bytes.
func cell(style lipgloss.Style, text string, w int) string {
	return style.Render(cellPlain(text, w))
}

// cellPlain is cell without the styling, for a header row.
func cellPlain(text string, w int) string {
	r := []rune(text)
	for lipgloss.Width(string(r)) > w {
		r = r[:len(r)-1]
	}

	return string(r) + strings.Repeat(" ", w-lipgloss.Width(string(r)))
}

// LiveFor is how recently a seat must have done something to be shown by
// name. This laptop has fifty-odd session directories, nearly all of them
// finished days ago; a board of corpses hides the seats that are actually
// working.
const LiveFor = 6 * time.Hour

// live splits the seats into those worth a row and a count of the rest.
func live(seats []plugin.Seat) (shown []plugin.Seat, idle int) {
	for _, s := range seats {
		last := s.StateAt
		if s.ArmedAt.After(last) {
			last = s.ArmedAt
		}

		if s.Closed && !s.Listening {
			idle++
			continue
		}

		if s.Me || s.Listening || (!last.IsZero() && time.Since(last) < LiveFor) {
			shown = append(shown, s)
			continue
		}

		idle++
	}

	return shown, idle
}

// seatsPanel draws who is on this machine.
func seatsPanel(seats []plugin.Seat, cursor int, width int) string {
	shown, idle := live(seats)

	var b strings.Builder
	b.WriteString(bright.Render("SEATS ON THIS MACHINE") + dim.Render(fmt.Sprintf("  %d here", len(shown))))
	if idle > 0 {
		b.WriteString(dim.Render(fmt.Sprintf(" · %d idle, not shown", idle)))
	}

	b.WriteString("\n" + rule.Render(strings.Repeat("─", max(20, min(width, 96)))) + "\n")
	b.WriteString(dim.Render("    "+cellPlain("HANDLE", 14)+cellPlain("STATE", 11)+cellPlain("LISTENER", 12)+cellPlain("FOLLOWS", 8)+cellPlain("BEHIND", 8)+"SESSION") + "\n")

	for i, s := range shown {
		handle, style := s.Handle, bright
		switch {
		case handle == "":
			handle, style = "(unnamed)", dim
		case s.Me:
			style = amber
		}

		listener, lstyle := "—", dim
		switch {
		case s.Listening:
			listener, lstyle = "armed "+ago(s.ArmedAt), green
		case !s.ArmedAt.IsZero():
			listener = "gone"
		}

		behind, bstyle := "·", dim
		if s.Behind > 0 {
			behind, bstyle = fmt.Sprint(s.Behind), amber
		}

		state := s.State
		if state == "" {
			state = "—"
		}

		marker := "  "
		if i == cursor {
			marker = invert.Render("▶ ")
		}

		b.WriteString(marker + stateDot(s) + " " +
			cell(style, handle, 14) +
			cell(dim, state, 11) +
			cell(lstyle, listener, 12) +
			cell(dim, fmt.Sprint(s.Follows), 8) +
			cell(bstyle, behind, 8) +
			dim.Render(short(s.Session)) + "\n")
	}

	return b.String()
}

// controlsPanel is where the knobs are, and it says what each one does
// rather than assuming the reader remembers.
func controlsPanel(env plugin.Env, cursor int, width int) string {
	var b strings.Builder
	b.WriteString(bright.Render("CONTROLS") + "\n")
	b.WriteString(rule.Render(strings.Repeat("─", max(20, min(width, 92)))) + "\n")

	handle := plugin.Participant(env)
	shown := amber.Render(handle)
	if handle == "" {
		shown = dim.Render("(unnamed — others see this machine's identity)")
	}

	b.WriteString(fmt.Sprintf("  %s %-16s %s\n", pointer(cursor, 0), "handle", shown))
	b.WriteString(dim.Render("     what this session speaks under; e to change") + "\n\n")

	for i, f := range plugin.Features {
		on := plugin.Enabled(env, f.Name)
		state := dim.Render("off")
		if on {
			state = green.Render("ON ")
		}

		b.WriteString(fmt.Sprintf("  %s %-16s %s  %s\n", pointer(cursor, i+1), f.Name, state, dim.Render(f.What)))
	}

	b.WriteString("\n" + dim.Render("  space toggles · e edits the handle · the default for every one of these is off") + "\n")
	return b.String()
}

func pointer(cursor, at int) string {
	if cursor == at {
		return invert.Render("▶")
	}

	return " "
}

func short(session string) string {
	if len(session) > 8 {
		return session[:8]
	}

	return session
}

func max(a, b int) int {
	if a > b {
		return a
	}

	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}

	return b
}

// handleKey types a handle. It is the one editable field, because it is
// the one knob whose value is not a yes or a no.
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.editing, m.handle = false, ""
		return m, nil
	case tea.KeyEnter:
		m.editing = false
		name := strings.TrimSpace(m.handle)
		if name == "" {
			m.flash = "a handle is required"
			return m, nil
		}

		updated, err := plugin.SetParticipant(m.env, name)
		if err != nil {
			m.flash = err.Error()
			return m, nil
		}

		m.flash = fmt.Sprintf("speaking as %s; %d conversation(s) updated, later joins inherit it", name, updated)
		m.seats = plugin.Seats(m.env)
		return m, nil
	case tea.KeyBackspace:
		if m.handle != "" {
			m.handle = m.handle[:len(m.handle)-1]
		}
	case tea.KeyRunes:
		m.handle += string(msg.Runes)
	}

	return m, nil
}

// rowCount is how far the cursor may travel on the screen in front of it.
func (m model) rowCount() int {
	switch m.view {
	case viewSeats:
		return len(m.seats)
	case viewControls:
		return len(plugin.Features) + 1 // the handle, then each feature
	}

	return len(m.visible())
}

func onOff(on bool) string {
	if on {
		return "ON"
	}

	return "off"
}
