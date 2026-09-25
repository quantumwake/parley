// Package tui is a k9s-style keyboard UI for parley. It only calls existing
// plugin list/read functions and draws the result. Optional: `parley tui`.
package tui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/quantumwake/parley/pkg/plugin"
)

const streamTail = 80

type view int

const (
	viewSeats    view = iota // who is on this machine, and what each is doing
	viewControls             // the knobs: the handle, the optional paths
	viewList                 // conversations
	viewStream               // one conversation, for context only
)

type model struct {
	env    plugin.Env
	who    string
	view   view
	rows   []plugin.SharedRow
	filter string
	cursor int
	err    string
	width  int
	height int

	streamName string
	stream     string
	filtering  bool

	seats   []plugin.Seat
	editing bool   // typing a handle
	handle  string // what has been typed
	flash   string // what just happened, said once
}

type listMsg struct {
	rows []plugin.SharedRow
	err  error
}

type streamMsg struct {
	name, text string
	err        error
}

func loadList(env plugin.Env) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		rows, err := plugin.ListSharedRows(ctx, env, "", "")
		return listMsg{rows: rows, err: err}
	}
}

func loadStream(env plugin.Env, name, id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		from := int64(0)
		if id != "" {
			if st, err := plugin.StoreFromEnv(env); err == nil {
				if head, err := st.Head(ctx, id); err == nil && head > streamTail {
					from = int64(head) - streamTail
				}
			}
		}
		var buf bytes.Buffer
		err := plugin.Read(ctx, env, name, from, true, 0, &buf)
		return streamMsg{name: name, text: buf.String(), err: err}
	}
}

type tickMsg time.Time

// tick refreshes the seats: they are files, so this costs nothing and
// keeps a board of live seats actually live.
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	m.seats = plugin.Seats(m.env)
	return tea.Batch(loadList(m.env), tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.seats = plugin.Seats(m.env)
		return m, tick()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case listMsg:
		m.err = errString(msg.err)
		if msg.err == nil {
			m.rows = msg.rows
		}
		if m.cursor >= len(m.visible()) {
			m.cursor = 0
		}
		return m, nil
	case streamMsg:
		m.err = errString(msg.err)
		if msg.err == nil {
			m.streamName, m.stream = msg.name, msg.text
			m.view = viewStream
		}
		return m, nil
	case tea.KeyMsg:
		if m.filtering {
			return m.filterKey(msg)
		}

		if m.editing {
			return m.handleKey(msg)
		}

		switch msg.String() {
		case "1":
			m.view, m.cursor, m.flash = viewSeats, 0, ""
			m.seats = plugin.Seats(m.env)
			return m, nil
		case "2":
			m.view, m.cursor, m.flash = viewControls, 0, ""
			return m, nil
		case "3":
			m.view, m.cursor, m.flash = viewList, 0, ""
			return m, loadList(m.env)
		case "tab":
			m.view, m.cursor, m.flash = (m.view+1)%3, 0, ""
			if m.view == viewSeats {
				m.seats = plugin.Seats(m.env)
			}
			return m, nil
		case "e":
			if m.view == viewControls && m.cursor == 0 {
				m.editing, m.handle, m.flash = true, plugin.Participant(m.env), ""
			}
			return m, nil
		case " ", "space":
			if m.view == viewControls && m.cursor > 0 {
				f := plugin.Features[m.cursor-1]
				on := !plugin.Enabled(m.env, f.Name)
				if _, err := plugin.SetFeature(f.Name, on); err != nil {
					m.flash = err.Error()
					return m, nil
				}

				m.env = plugin.EnvFromProcess()
				m.flash = f.Name + " is " + onOff(on) + " for this machine"
				if f.Name == "doorbell" {
					m.flash += " — restart any `parley wait` for it to take effect"
				}
			}

			return m, nil
		case "q", "ctrl+c":
			if m.view == viewStream {
				m.view = viewList
				return m, nil
			}

			return m, tea.Quit
		case "esc":
			if m.view == viewStream {
				m.view = viewList
			}
			return m, nil
		case "r":
			m.seats = plugin.Seats(m.env)
			if m.view == viewStream && m.streamName != "" {
				id := ""
				for _, r := range m.rows {
					if r.Name == m.streamName {
						id = r.ID
						break
					}
				}
				return m, loadStream(m.env, m.streamName, id)
			}
			return m, loadList(m.env)
		case "/":
			m.filtering = true
			return m, nil
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor+1 < m.rowCount() {
				m.cursor++
			}
		case "enter":
			if m.view == viewList {
				vis := m.visible()
				if m.cursor >= 0 && m.cursor < len(vis) {
					return m, loadStream(m.env, vis[m.cursor].Name, vis[m.cursor].ID)
				}
			}
		}
	}
	return m, nil
}

func (m model) filterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyEnter:
		m.filtering = false
		m.cursor = 0
		return m, nil
	case tea.KeyBackspace:
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
		}
	case tea.KeyRunes:
		m.filter += string(msg.Runes)
	}
	return m, nil
}

func (m model) visible() []plugin.SharedRow {
	q := strings.ToLower(m.filter)
	if q == "" {
		return m.rows
	}
	var out []plugin.SharedRow
	for _, r := range m.rows {
		if strings.Contains(strings.ToLower(r.Name+" "+r.Description+" "+r.Access), q) {
			out = append(out, r)
		}
	}
	return out
}

func (m model) View() string {
	width := m.width
	if width <= 0 {
		width = 92
	}

	body := ""
	switch m.view {
	case viewSeats:
		if len(m.seats) == 0 {
			body = dim.Render("no sessions on this machine yet")
			break
		}

		body = seatsPanel(m.seats, m.cursor, width)
		body += "\n" + dim.Render("  a seat with no handle shows this machine's identity to everyone — press 2, then e")
	case viewControls:
		body = controlsPanel(m.env, m.cursor, width)
		if m.editing {
			body += "\n" + amber.Render("  handle: ") + m.handle + invert.Render(" ") + dim.Render("   enter to set · esc to cancel")
		}
	case viewStream:
		lines := strings.Split(strings.TrimRight(m.stream, "\n"), "\n")
		if m.stream == "" {
			body = dim.Render("(empty)")
			break
		}

		room := m.height - 6
		if room < 8 {
			room = 8
		}

		start := 0
		if len(lines) > room {
			start = len(lines) - room
		}

		body = strings.Join(lines[start:], "\n")
	default:
		vis := m.visible()
		if len(vis) == 0 {
			body = dim.Render("no conversations")
			break
		}

		var b strings.Builder
		b.WriteString(dim.Render(fmt.Sprintf("  %-28s %-10s %-10s  %s", "NAME", "ACCESS", "SUB", "DESCRIPTION")) + "\n")
		for i, r := range vis {
			line := fmt.Sprintf("  %-28s %-10s %-10s  %s", r.Name, r.Access, r.Subscribed, r.Description)
			if i == m.cursor {
				line = invert.Render(line)
			}

			b.WriteString(line + "\n")
		}

		body = b.String()
	}

	return m.chrome(width) + "\n" + body + "\n" + m.statusBar(width)
}

// chrome is the title rule: what this is, who is at it, and which screen
// is in front — the retro part, and it earns its space by saying where
// you are without a menu.
func (m model) chrome(width int) string {
	tabs := []string{"1 SEATS", "2 CONTROLS", "3 CHANNELS"}
	var rendered []string
	for i, t := range tabs {
		if view(i) == m.view || (m.view == viewStream && i == 2) {
			rendered = append(rendered, invert.Render(" "+t+" "))
			continue
		}

		rendered = append(rendered, dim.Render(" "+t+" "))
	}

	title := amber.Render("▛▀▘ PARLEY COMMAND") + dim.Render(" · "+m.who)
	if m.view == viewStream && m.streamName != "" {
		title += dim.Render(" · " + m.streamName)
	}

	line := title + "  " + strings.Join(rendered, "")
	return line + "\n" + rule.Render(strings.Repeat("━", max(20, min(width, 120))))
}

// statusBar says what just happened, or what the keys do.
func (m model) statusBar(width int) string {
	if m.err != "" {
		return rule.Render(strings.Repeat("─", max(20, min(width, 120)))) + "\n" + amber.Render(" "+m.err)
	}

	if m.flash != "" {
		return rule.Render(strings.Repeat("─", max(20, min(width, 120)))) + "\n" + green.Render(" ✓ "+m.flash)
	}

	keys := " tab/1-3 screens · j/k move · r refresh · q quit"
	switch {
	case m.filtering:
		keys = " /" + m.filter + "█"
	case m.editing:
		keys = " typing a handle · enter to set · esc to cancel"
	case m.view == viewControls:
		keys = " space toggles · e edits the handle · tab/1-3 screens · q quit"
	case m.view == viewList:
		keys = " enter opens · / filters · tab/1-3 screens · r refresh · q quit"
	}

	return rule.Render(strings.Repeat("─", max(20, min(width, 120)))) + "\n" + dim.Render(keys)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Run starts the TUI. Needs a real terminal.
func Run(env plugin.Env) error {
	who := plugin.Username(env)
	if who == "" {
		who = env.IdentityPath
	}
	p := tea.NewProgram(model{env: env, who: who}, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
