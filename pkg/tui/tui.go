// Package tui is a k9s-style keyboard UI for parley: conversations, then
// a stream. It is optional (`parley tui`); the CLI and browser console stay.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/quantumwake/parley/pkg/conversation"
	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/plugin"
	"github.com/quantumwake/parley/pkg/store"
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
	streamID   string
	posts      []string
	filtering  bool
}

type listMsg struct {
	rows []plugin.SharedRow
	err  error
}

type streamMsg struct {
	name, id string
	posts    []string
	err      error
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
		st, err := plugin.StoreFromEnv(env)
		if err != nil {
			return streamMsg{name: name, id: id, err: err}
		}
		head, err := st.Head(ctx, id)
		if err != nil {
			return streamMsg{name: name, id: id, err: err}
		}
		from := store.Position(0)
		if head > streamTail {
			from = head - streamTail
		}
		var posts []string
		for e, err := range conversation.Attach(st, id).Scan(ctx, from, 0) {
			if err != nil {
				return streamMsg{name: name, id: id, err: err}
			}
			posts = append(posts, formatLine(e))
		}
		return streamMsg{name: name, id: id, posts: posts}
	}
}

func formatLine(e event.Event) string {
	var m map[string]any
	_ = json.Unmarshal(e.Content, &m)
	text, _ := m["text"].(string)
	text = strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")
	if len(text) > 120 {
		text = text[:117] + "..."
	}
	who := e.Participant
	if who == "" {
		who = e.Identity
	}
	kind := strings.TrimPrefix(string(e.Kind), "post.")
	return fmt.Sprintf("%s  %s  %s", who, kind, text)
}

func (m model) Init() tea.Cmd { return loadList(m.env) }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case listMsg:
		m.rows, m.err = msg.rows, errString(msg.err)
		if m.cursor >= len(m.visible()) {
			m.cursor = 0
		}
		return m, nil
	case streamMsg:
		m.streamName, m.streamID, m.posts, m.err = msg.name, msg.id, msg.posts, errString(msg.err)
		m.view = viewStream
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
			if m.view == viewStream && m.streamID != "" {
				return m, loadStream(m.env, m.streamName, m.streamID)
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
					row := vis[m.cursor]
					return m, loadStream(m.env, row.Name, row.ID)
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
		if len(m.posts) == 0 {
			body = "(empty)"
		} else {
			start := 0
			max := m.height - 4
			if max < 8 {
				max = 8
			}
			if len(m.posts) > max {
				start = len(m.posts) - max
			}
			body = strings.Join(m.posts[start:], "\n")
		}
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
