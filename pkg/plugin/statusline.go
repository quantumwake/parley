package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// StatusLineWho is the line `parley statusline` prints: the handle this
// session speaks under, then the identity it speaks as, for example
//
//	parley-delivery · krasaee-macbook-pro-40974ff47cb7f629
//
// It answers the question every seat on one machine kept having to ask: who
// is this session. A session with no handle says so, in yellow, because a
// missing handle is what leaves seven sessions speaking as one name; one
// with no session at all says that; an identity that is not enrolled says
// that. Local files only, because a status line is redrawn constantly.
func StatusLineWho(env Env, w io.Writer) error {
	who := authorOf(env)
	if who == "" {
		who = "not enrolled"
	}

	var head string

	switch {
	case env.Session == "":
		head = ansiDim + "no session" + ansiReset
	case Participant(env) == "":
		head = ansiYellow + "no handle" + ansiReset
	default:
		head = Participant(env)
	}

	_, err := fmt.Fprintf(w, "%s · %s%s%s\n", head, ansiDim, who, ansiReset)

	return err
}

// statusSessionID is what a session id may look like when it arrives on the
// status line's stdin: it becomes a directory name, so nothing that could
// leave the sessions directory is accepted.
var statusSessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// SessionFromStatusInput reads the session id Claude Code hands its status
// line command as JSON on stdin ({"session_id": ...}). The command's own
// environment does not carry it, so without this the line cannot tell which
// session it is drawn for. It returns "" for anything that is not a valid id,
// and it gives up after wait, so a stdin that never delivers cannot stall a
// redraw.
func SessionFromStatusInput(r io.Reader, wait time.Duration) string {
	got := make(chan string, 1)

	go func() {
		var in struct {
			SessionID      string `json:"session_id"`
			SessionIDCamel string `json:"sessionId"`
			ConversationID string `json:"conversation_id"`
		}

		if json.NewDecoder(io.LimitReader(r, 1<<16)).Decode(&in) != nil {
			got <- ""
			return
		}

		id := ""
		for _, candidate := range []string{in.SessionID, in.SessionIDCamel, in.ConversationID} {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" {
				id = candidate
				break
			}
		}
		if !statusSessionID.MatchString(id) {
			id = ""
		}

		got <- id
	}()

	select {
	case id := <-got:
		return id
	case <-time.After(wait):
		return ""
	}
}
