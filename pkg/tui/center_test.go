package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/quantumwake/parley/pkg/plugin"
)

func init() {
	// Pin the profile so a test sees the same escapes on every machine,
	// coloured rather than plain: the column widths are only wrong when
	// styling is on, and an uncoloured test would never catch it.
	lipgloss.SetColorProfile(termenv.ANSI256)
}

var escapes = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// visible is the row as an eye sees it, with the escapes taken out.
func visible(s string) string { return escapes.ReplaceAllString(s, "") }

// The first screen answers the question three commands used to: who is
// here, what do they call themselves, and what are they doing.
func TestTheSeatsPanelSaysWhoIsHereAndWhatTheyAreDoing(t *testing.T) {
	seats := []plugin.Seat{
		{Session: "aaaaaaaa-1111", Handle: "champion", State: "working", StateAt: time.Now(), Me: true, Follows: 9, Listening: true, ArmedAt: time.Now().Add(-90 * time.Second)},
		{Session: "bbbbbbbb-2222", Handle: "reviewer", State: "listening", StateAt: time.Now().Add(-time.Minute), Follows: 3},
		{Session: "cccccccc-3333", State: "", Follows: 1, StateAt: time.Now().Add(-2 * time.Minute)},
	}

	out := seatsPanel(seats, 0, 100)
	for _, want := range []string{"champion", "reviewer", "working", "armed", "(unnamed)", "SEATS ON THIS MACHINE"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the seats panel does not say %q:\n%s", want, out)
		}
	}
}

// A seat that never chose a handle is called out rather than shown as a
// blank, because that is the state that made thirteen seats look alike.
func TestAnUnnamedSeatIsVisiblyUnnamed(t *testing.T) {
	out := seatsPanel([]plugin.Seat{{Session: "dddddddd-4444", StateAt: time.Now()}}, 0, 100)
	if !strings.Contains(out, "(unnamed)") {
		t.Fatalf("an unnamed seat is not called out:\n%s", out)
	}
}

// A listener that died leaves its state file behind; the panel must say
// "gone" rather than "armed", or a seat looks like it is listening when
// nothing is.
func TestAListenerThatDiedIsNotShownAsArmed(t *testing.T) {
	out := seatsPanel([]plugin.Seat{{
		Session: "eeeeeeee-5555", Handle: "grok",
		ArmedAt: time.Now().Add(-time.Hour), Listening: false,
	}}, 0, 100)

	if strings.Contains(out, "armed") {
		t.Fatalf("a dead listener is shown as armed:\n%s", out)
	}

	if !strings.Contains(out, "gone") {
		t.Fatalf("a dead listener is not called out:\n%s", out)
	}
}

// The controls screen is the knobs: it names each one, says whether it is
// on here, and says what it does — a switch a reader cannot explain is a
// switch nobody touches.
func TestTheControlsPanelNamesEveryKnobAndWhatItDoes(t *testing.T) {
	env := plugin.Env{}
	out := controlsPanel(env, 0, 100)
	if !strings.Contains(out, "handle") {
		t.Fatalf("the controls do not offer the handle:\n%s", out)
	}

	for _, f := range plugin.Features {
		if !strings.Contains(out, f.Name) {
			t.Fatalf("the controls do not offer %q:\n%s", f.Name, out)
		}

		if !strings.Contains(out, f.What) {
			t.Fatalf("%q is offered with no explanation:\n%s", f.Name, out)
		}
	}
}

// A session with no handle is told on the controls screen what that means
// for everyone else, not just shown an empty field.
func TestTheControlsSayWhatHavingNoHandleCosts(t *testing.T) {
	out := controlsPanel(plugin.Env{}, 0, 100)
	if !strings.Contains(out, "identity") {
		t.Fatalf("the controls do not say what an unnamed seat looks like:\n%s", out)
	}
}

// This laptop carries fifty-odd session directories, nearly all of them
// finished days ago. A board of corpses hides the seats that are working,
// so an old seat becomes a number instead of a row - a number, because
// silently dropping them would make the panel lie about what is here.
func TestSeatsThatWentQuietLongAgoBecomeACountNotRows(t *testing.T) {
	seats := []plugin.Seat{
		{Session: "aaaaaaaa-1111", Handle: "champion", State: "working", StateAt: time.Now(), Me: true},
		{Session: "bbbbbbbb-2222", Handle: "yesterday", StateAt: time.Now().Add(-30 * time.Hour)},
		{Session: "cccccccc-3333", StateAt: time.Now().Add(-49 * time.Hour)},
	}

	out := seatsPanel(seats, 0, 100)
	if strings.Contains(out, "yesterday") {
		t.Fatalf("a seat quiet for 30h still takes a row:\n%s", out)
	}

	if !strings.Contains(out, "2 idle") {
		t.Fatalf("the seats left out are not counted:\n%s", out)
	}
}

// A seat that armed a listener overnight and is still waiting is here,
// however old its timestamps are: a live listener is presence by itself.
// Both times are older than the idle window, so only the live listener
// can keep this row on the board.
func TestASeatWaitingSinceLastNightIsStillShown(t *testing.T) {
	out := seatsPanel([]plugin.Seat{{
		Session: "eeeeeeee-5555", Handle: "keywake",
		StateAt:   time.Now().Add(-40 * time.Hour),
		Listening: true, ArmedAt: time.Now().Add(-14 * time.Hour),
	}}, 0, 100)

	if !strings.Contains(out, "keywake") {
		t.Fatalf("an armed seat was filtered out as idle:\n%s", out)
	}
}

// Every cell is padded before it is styled. Padding afterwards counts the
// escape codes as characters, so a coloured column eats its own padding
// and every column to its right goes ragged - which is exactly how the
// first render of this board came out.
func TestColumnsLineUpWhenTheCellsAreColoured(t *testing.T) {
	seats := []plugin.Seat{
		{Session: "aaaaaaaa-1111", Handle: "champion", State: "working", StateAt: time.Now(), Me: true, Listening: true, ArmedAt: time.Now(), Behind: 4},
		{Session: "bbbbbbbb-2222", Handle: "ci", State: "idle", StateAt: time.Now()},
		{Session: "cccccccc-3333", StateAt: time.Now()},
	}

	rows := strings.Split(strings.TrimRight(seatsPanel(seats, 0, 100), "\n"), "\n")
	if len(rows) != 6 {
		t.Fatalf("expected a title, a rule, a header and three seats, got %d rows:\n%s", len(rows), visible(strings.Join(rows, "\n")))
	}

	want := column(visible(rows[2]), "SESSION")
	for _, row := range rows[3:] {
		plain := visible(row)
		at := -1
		for _, id := range []string{"aaaaaaaa", "bbbbbbbb", "cccccccc"} {
			if c := column(plain, id); c >= 0 {
				at = c
				break
			}
		}

		if at != want {
			t.Fatalf("the session column starts at %d, the header puts it at %d:\n%s", at, want, visible(strings.Join(rows, "\n")))
		}
	}
}

// column is where a word starts on screen: the count of columns before
// it, not the count of bytes, so a row carrying a dash or a dot measures
// the same as a plain one.
func column(row, word string) int {
	i := strings.Index(row, word)
	if i < 0 {
		return -1
	}

	return lipgloss.Width(row[:i])
}

// Nothing clears presence when a terminal is closed, so a seat that left
// hours ago still reads as "listening" on disk. Once its session process
// is gone the board must stop counting it as here, however fresh the
// timestamp looks.
func TestASeatWhoseTerminalWasClosedIsNotShownAsHere(t *testing.T) {
	seats := []plugin.Seat{
		{Session: "aaaaaaaa-1111", Handle: "champion", State: "working", StateAt: time.Now(), Me: true},
		{Session: "bbbbbbbb-2222", Handle: "ghost", State: "listening", StateAt: time.Now(), Closed: true},
	}

	out := seatsPanel(seats, 0, 100)
	if strings.Contains(out, "ghost") {
		t.Fatalf("a closed session still takes a row:\n%s", out)
	}

	if !strings.Contains(out, "1 idle") {
		t.Fatalf("the closed session is not counted:\n%s", out)
	}
}

// Sessions that started before presence recorded an owner have no pid to
// check. Proving nothing must not hide them, or the board would empty out
// the moment it shipped.
func TestASeatWithNoRecordedOwnerIsStillShown(t *testing.T) {
	out := seatsPanel([]plugin.Seat{{
		Session: "cccccccc-3333", Handle: "older", State: "listening",
		StateAt: time.Now(), Closed: false,
	}}, 0, 100)

	if !strings.Contains(out, "older") {
		t.Fatalf("a seat with no owner recorded was hidden:\n%s", out)
	}
}

// A wait outlives the session that armed it. If the listener still holds
// its lock something is running, so the seat stays on the board.
func TestAClosedSeatWithALiveListenerIsStillShown(t *testing.T) {
	out := seatsPanel([]plugin.Seat{{
		Session: "dddddddd-4444", Handle: "orphan", State: "listening",
		StateAt: time.Now().Add(-40 * time.Hour), Closed: true,
		Listening: true, ArmedAt: time.Now().Add(-time.Minute),
	}}, 0, 100)

	if !strings.Contains(out, "orphan") {
		t.Fatalf("a live listener was hidden because its session closed:\n%s", out)
	}
}
