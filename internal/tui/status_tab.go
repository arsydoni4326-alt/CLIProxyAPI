package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// statusTabModel displays the server status including version, uptime, and watcher state.
type statusTabModel struct {
	client   *Client
	viewport viewport.Model
	status   map[string]any
	err      error
	width    int
	height   int
	ready    bool
}

type statusPollMsg struct {
	status map[string]any
	err    error
}

type statusTickMsg struct{}

func newStatusTabModel(client *Client) statusTabModel {
	return statusTabModel{
		client: client,
	}
}

func (m statusTabModel) Init() tea.Cmd {
	return m.fetchStatus
}

func (m statusTabModel) View() string {
	if !m.ready {
		return T("loading")
	}
	return m.viewport.View()
}

func (m *statusTabModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
	m.viewport.SetContent(m.renderContent())
}

func (m statusTabModel) fetchStatus() tea.Msg {
	status, err := m.client.GetStatus()
	return statusPollMsg{status: status, err: err}
}

func (m statusTabModel) waitForNextPoll() tea.Cmd {
	return tea.Tick(5*time.Second, func(_ time.Time) tea.Msg {
		return statusTickMsg{}
	})
}

func (m statusTabModel) Update(msg tea.Msg) (statusTabModel, tea.Cmd) {
	switch msg := msg.(type) {
	case localeChangedMsg:
		m.viewport.SetContent(m.renderContent())
		return m, nil

	case statusPollMsg:
		m.status = msg.status
		m.err = msg.err
		m.viewport.SetContent(m.renderContent())
		m.viewport.GotoTop()
		return m, m.waitForNextPoll()

	case statusTickMsg:
		return m, m.fetchStatus
	}

	return m, nil
}

func (m statusTabModel) renderContent() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render(T("status_title")))
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render(fmt.Sprintf("[r] %s • [↑↓] %s", T("refresh"), T("scroll"))))
	sb.WriteString("\n\n")

	if m.err != nil {
		sb.WriteString(errorStyle.Render(fmt.Sprintf("%s %v", T("error_prefix"), m.err)))
		sb.WriteString("\n")
		return sb.String()
	}

	if m.status == nil {
		sb.WriteString(fmt.Sprintf("  %s...", T("loading")))
		sb.WriteString("\n")
		return sb.String()
	}

	// Version
	if v, ok := m.status["version"]; ok && v != nil {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", subtitleStyle.Render("Version"), fmt.Sprintf("%v", v)))
	}

	// Started at
	if startedAt, ok := m.status["started_at"]; ok && startedAt != nil {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", subtitleStyle.Render("Started At"), fmt.Sprintf("%v", startedAt)))
	}

	// Commit & build info
	if commit, ok := m.status["commit"]; ok && commit != nil && fmt.Sprintf("%v", commit) != "" {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", subtitleStyle.Render("Commit"), fmt.Sprintf("%v", commit)))
	}
	if buildDate, ok := m.status["build_date"]; ok && buildDate != nil && fmt.Sprintf("%v", buildDate) != "" {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", subtitleStyle.Render("Build Date"), fmt.Sprintf("%v", buildDate)))
	}

	// Uptime
	if uptime, ok := m.status["uptime"]; ok && uptime != nil {
		sb.WriteString(fmt.Sprintf("  %s: %s\n", subtitleStyle.Render("Uptime"), fmt.Sprintf("%v", uptime)))
	}

	// Config watcher state
	if watcherState, ok := m.status["watcher_state"]; ok {
		watcherLabel := subtitleStyle.Render("Config Watcher")
		switch v := watcherState.(type) {
		case bool:
			if v {
				sb.WriteString(fmt.Sprintf("  %s: %s\n", watcherLabel, successStyle.Render(T("status_active"))))
			} else {
				sb.WriteString(fmt.Sprintf("  %s: %s\n", watcherLabel, errorStyle.Render(T("status_disabled"))))
			}
		default:
			sb.WriteString(fmt.Sprintf("  %s: %s\n", watcherLabel, fmt.Sprintf("%v", v)))
		}
	}

	return sb.String()
}
