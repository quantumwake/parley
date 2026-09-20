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
	"github.com/charmbracelet/lipgloss"

	"github.com/quantumwake/parley/pkg/plugin"
)

const streamTail = 80

type view int

const (
	viewList view = iota
	viewStream
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

func (m model) Init() tea.Cmd { return loadList(m.env) }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
		switch msg.String() {
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
			if m.cursor+1 < len(m.visible()) {
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
	header := lipgloss.NewStyle().Bold(true).Render("parley tui") + "  " + m.who + "  (not console)"
	help := "j/k enter  esc  / filter  r refresh  q quit"
	if m.filtering {
		help = "/" + m.filter + "█"
	}
	if m.err != "" {
		help = m.err
	}

	body := ""
	switch m.view {
	case viewStream:
		header += "  " + m.streamName
		lines := strings.Split(strings.TrimRight(m.stream, "\n"), "\n")
		if m.stream == "" {
			body = "(empty)"
			break
		}
		max := m.height - 4
		if max < 8 {
			max = 8
		}
		start := 0
		if len(lines) > max {
			start = len(lines) - max
		}
		body = strings.Join(lines[start:], "\n")
	default:
		vis := m.visible()
		if len(vis) == 0 {
			body = "no conversations"
			break
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%-28s %-10s %-10s  %s\n", "NAME", "ACCESS", "SUB", "DESCRIPTION")
		for i, r := range vis {
			line := fmt.Sprintf("  %-26s %-10s %-10s  %s", r.Name, r.Access, r.Subscribed, r.Description)
			if i == m.cursor {
				line = lipgloss.NewStyle().Reverse(true).Render(line)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		body = b.String()
	}

	return header + "\n\n" + body + "\n" + lipgloss.NewStyle().Faint(true).Render(help)
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
