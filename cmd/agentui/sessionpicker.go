package agentui

import (
	"context"
	"fmt"
	"image/color"
	"io"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/support/fsutil"
	toolcore "github.com/invokr-ai/invokr/tools/core"

	"github.com/antonikliment/tuikit"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/surface/ui"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// ErrPickCanceled reports that the user dismissed the picker without choosing.
// Callers exit rather than starting a session, which is what dismissing a
// resume prompt means — the footer offers it as "quit".
var ErrPickCanceled = fmt.Errorf("session selection canceled")

// cwdColumn is how wide the directory column grows before it is truncated.
const cwdColumn = 34

// Picker tabs: the default view is this directory, the second is everything.
const (
	tabCwd = iota
	tabAll
)

type pickerModel struct {
	list  tuikit.SelectList
	store *sessionstore.Store
	// all is every stored session; the active tab decides which of them the
	// list currently holds.
	all []sessionstore.SessionInfo
	// cwd is the workspace root the first tab filters by.
	cwd string
	tab int
	// sessions carries the fields the rows draw but tuikit.SelectItem has no
	// place for — the last message and the timestamp the date grouping needs —
	// keyed by session ID.
	sessions map[string]sessionstore.SessionInfo
	// purge removes a session everywhere it is stored, not just the transcript.
	purge func(string) error
	// status arms the destructive delete: "x" once to arm, "x" again to confirm.
	status tuikit.Status
	// now is injectable so the date grouping is testable without a clock.
	now           func() time.Time
	chosen        string
	width, height int
}

func (m pickerModel) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.BackgroundColorMsg:
		ui.Invokr = ui.NewTheme(msg.IsDark())
		m.list.SetTheme(ui.Invokr.Palette)
		return m, nil
	case tea.KeyPressMsg:
		// While filtering, keys belong to the filter input, not to us.
		if m.list.Filtering() {
			m.list.Update(msg)
			return m, nil
		}
		return m.updateKey(msg)
	}
	return m, nil
}

func (m pickerModel) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if id := m.list.SelectedKey(); id != "" {
			m.chosen = id
			return m, tea.Quit
		}
	case "x":
		if id := m.list.SelectedKey(); id != "" {
			return m, m.status.Confirm(id, false, func() tea.Cmd { return m.deleteCmd(id) })
		}
	case "tab", "shift+tab":
		m.status.Disarm()
		m.setTab(1 - m.tab)
		return m, nil
	case "esc", "q", "ctrl+c":
		if m.status.Pending() != "" {
			m.status.Disarm()
			return m, nil
		}
		return m, tea.Quit // chosen stays empty, meaning canceled
	default:
		m.status.Disarm()
		m.list.Update(msg)
	}
	return m, nil
}

// deleteCmd purges the armed session and re-reads the store. Re-listing rather
// than removing by index is deliberate: the cursor is relative to the filtered
// view, so re-reading is the only always-correct way to reflect what is on disk.
func (m *pickerModel) deleteCmd(id string) tea.Cmd {
	if err := m.purge(id); err != nil {
		m.status.SetError("delete failed: " + err.Error())
		return nil
	}
	sessions, err := m.store.List()
	if err != nil || len(sessions) == 0 {
		return tea.Quit
	}
	m.all = sessions
	m.setTab(m.tab)
	return nil
}

// setTab switches the active tab and reloads the list from m.all. Rebuilding
// rather than hiding rows keeps tuikit.SelectList's cursor and filter state
// consistent with what is actually on screen.
func (m *pickerModel) setTab(tab int) {
	m.tab = tab
	sessions := m.tabSessions()
	m.list.SetItems(sessionItems(sessions, m.now()))
	m.sessions = sessionsByID(sessions)
}

// tabSessions is the slice the active tab shows.
func (m pickerModel) tabSessions() []sessionstore.SessionInfo {
	if m.tab == tabAll {
		return m.all
	}
	return sessionstore.FilterByCwd(m.all, m.cwd)
}

func sessionsByID(sessions []sessionstore.SessionInfo) map[string]sessionstore.SessionInfo {
	byID := make(map[string]sessionstore.SessionInfo, len(sessions))
	for _, info := range sessions {
		byID[info.ID] = info
	}
	return byID
}

func (m pickerModel) View() tea.View {
	// Alt screen for the same reason the main UI uses it: the picker owns the
	// whole screen and must not leave half-painted rows in scrollback.
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m pickerModel) render() string {
	width, height := max(m.width, 40), max(m.height, 8)
	tabs := m.tabStrip()
	head := ui.Invokr.Muted.Render(m.list.Counter() + " sessions · sorted by recent")
	if query := m.list.Query(); query != "" || m.list.Filtering() {
		head += "  " + ui.Invokr.Info.Render("/"+query)
	}

	footer := ui.Invokr.Muted.Render("↑↓ move · enter resume · tab switch · / filter · x delete · q back · ctrl+c quit")
	if m.status.Pending() != "" {
		footer = ui.Invokr.Danger.Render("Delete this session and its memory?") + "  " + ui.Invokr.Muted.Render("x again to confirm · esc cancel")
	}
	rows := m.status.AppendRows(ui.Invokr.Palette, nil)

	tabLines := strings.Count(tabs, "\n") + 1
	body := m.listView(width, height-3-tabLines-len(rows))
	return strings.Join(append([]string{tabs, head, body}, append(rows, footer)...), "\n")
}

// tabStrip draws the two views as folder tabs, each labelled with its count.
func (m pickerModel) tabStrip() string {
	titles := []string{fmt.Sprintf("This directory (%d)", len(sessionstore.FilterByCwd(m.all, m.cwd))), fmt.Sprintf("All (%d)", len(m.all))}
	brand := ui.Invokr.Palette.Brand
	return ui.Invokr.Palette.TabStrip(titles, []color.Color{brand, brand}, m.tab)
}

// PickSession runs an interactive picker over stored sessions and returns the
// chosen session ID. It returns ErrPickCanceled when the user dismisses it.
func PickSession(ctx context.Context, store *sessionstore.Store, purge func(string) error, in io.Reader, out io.Writer) (string, error) {
	sessions, err := store.List()
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions found")
	}
	if !fsutil.IsTerminal(in) {
		return "", fmt.Errorf("cannot show the session picker without a terminal; pass --resume=<session-id>")
	}

	picker := pickerModel{
		list: tuikit.NewSelectList(ui.Invokr.Palette), store: store, all: sessions,
		cwd: store.Cwd(), purge: purge, now: time.Now, width: 96, height: 24,
	}
	// Always open on this directory, even when it is empty: listView says so,
	// and the tab counts show where the rest of the sessions went.
	picker.setTab(tabCwd)
	final, err := tea.NewProgram(picker, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return "", err
	}
	model, ok := final.(pickerModel)
	if !ok || model.chosen == "" {
		return "", ErrPickCanceled
	}
	return model.chosen, nil
}

// sessionItems adapts stored sessions into picker rows. The label carries the
// generated title, the trailing column how many messages, how long ago it was
// touched, and enough of the UUID to recognize and to pass back to --resume.
func sessionItems(sessions []sessionstore.SessionInfo, now time.Time) []tuikit.SelectItem {
	return toolcore.MapSlice(sessions, func(info sessionstore.SessionInfo) tuikit.SelectItem {
		return tuikit.SelectItem{
			Key:      info.ID,
			Label:    info.Label(),
			Trailing: fmt.Sprintf("%d msgs · %s · %.8s", info.Messages, sessionAge(info.UpdatedAt, now), info.ID),
		}
	})
}

// sessionAge is the design's short relative age. tuikit.Age gives up past its
// 24-hour window and prints a full RFC 3339 timestamp, which is both unreadable
// in a picker row and wider than the column.
func sessionAge(at, now time.Time) string {
	switch delta := now.Sub(at); {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm ago", int(delta.Minutes()))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(delta.Hours()))
	case at.Year() == now.Year():
		return at.Format("Jan 2")
	default:
		return at.Format("Jan 2 2006")
	}
}

// sessionSection is the date-group header a row falls under.
func sessionSection(at, now time.Time) string {
	day := func(t time.Time) time.Time {
		year, month, date := t.Date()
		return time.Date(year, month, date, 0, 0, 0, 0, t.Location())
	}
	switch days := int(day(now).Sub(day(at.In(now.Location()))).Hours() / 24); {
	case days <= 0:
		return "TODAY"
	case days == 1:
		return "YESTERDAY"
	case at.Year() == now.Year():
		return strings.ToUpper(at.Format("Jan 2"))
	default:
		return strings.ToUpper(at.Format("Jan 2 2006"))
	}
}

// listView draws the design's picker body: date-group headers, then two lines
// per session — the title with its age right-aligned, and a preview of where
// the conversation left off.
//
// tuikit.SelectList renders one line per row and has no notion of a section, so
// the picker keeps it as the owner of filter and cursor state and lays the rows
// out here.
func (m pickerModel) listView(width, height int) string {
	rows := m.list.Filtered()
	if len(rows) == 0 {
		if m.tab == tabCwd && len(m.all) > 0 && m.list.Query() == "" {
			return ui.Invokr.Muted.Render("no sessions in this directory · tab for all")
		}
		return ui.Invokr.Muted.Render("no matches")
	}
	selected := m.list.SelectedKey()
	lines, cursorLine, section := make([]string, 0, len(rows)*3), 0, ""
	for _, item := range rows {
		info := m.sessions[item.Key]
		if head := sessionSection(info.UpdatedAt, m.now()); head != section {
			section = head
			lines = append(lines, sectionRule(head, width))
		}
		if item.Key == selected {
			cursorLine = len(lines)
		}
		if m.tab != tabAll {
			info.Cwd = "" // every row is this directory; the column would be noise
		}
		lines = append(lines, sessionEntryLines(info, item, item.Key == selected, width)...)
	}
	return strings.Join(visibleWindow(lines, cursorLine, height), "\n")
}

// sessionEntryLines is one session: the title row, then the "last:" detail.
func sessionEntryLines(info sessionstore.SessionInfo, item tuikit.SelectItem, selected bool, width int) []string {
	marker, label := "    ", ui.Invokr.Muted
	if selected {
		marker, label = "  "+ui.Invokr.Accent.Render("› "), ui.Invokr.Text.Bold(true)
	}
	// The directory is a tertiary column ahead of the trailing metadata: the
	// absolute path, middle-truncated so both ends stay readable. TrimLeft
	// collapses the column away for rows that carry no directory.
	dir := tuikit.TruncMiddle(info.Cwd, cwdColumn)
	meta := strings.TrimLeft(dir+strings.Repeat(" ", max(1, cwdColumn-ansi.StringWidth(dir)+1))+item.Trailing, " ")
	title := tuikit.TruncMiddle(item.Label, max(1, width-ansi.StringWidth(marker+meta)-2))
	gap := max(1, width-ansi.StringWidth(marker)-ansi.StringWidth(title)-ansi.StringWidth(meta))
	lines := []string{marker + label.Render(title) + strings.Repeat(" ", gap) + ui.Invokr.Muted.Faint(true).Render(dir) + ui.Invokr.Muted.Render(strings.TrimPrefix(meta, dir))}
	if last := strings.TrimSpace(info.Last); last != "" {
		detail := `last: "` + strings.Join(strings.Fields(last), " ") + `"`
		lines = append(lines, "      "+ui.Invokr.Muted.Render(ansi.Truncate(detail, max(1, width-6), "…")))
	}
	return lines
}

// visibleWindow slides a fixed-height window over the rendered lines so the
// cursor stays inside it. Entries are multi-line, so the window is over lines
// rather than over sessions.
func visibleWindow(lines []string, cursor, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	offset := min(max(0, cursor-height/2), len(lines)-height)
	return lines[offset : offset+height]
}
