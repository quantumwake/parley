package plugin

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/quantumwake/parley/pkg/agentaccess"
)

// joinProjectChannels follows, in this session, each of its projects'
// channels it does not follow yet, so a machine made for a project comes up
// listening with nobody touching it (board item 6, champion @903).
//
// Only what the identity can already read is joined: a channel its grants do
// not cover is refused by the store and skipped. A channel this session left
// by hand stays left. Each join is a line in the seat's log. When anything
// was joined and the seat was not disarmed, the session is armed, so the
// start asks the agent for its wait. It answers the names joined.
// maxStartJoins bounds the channels one session start joins, as the text it
// injects is bounded: a project list cannot fill a seat's follows.
const maxStartJoins = 8

func joinProjectChannels(ctx context.Context, env Env, projects []agentaccess.AgentProject) []string {
	if env.Session == "" {
		return nil
	}

	following := map[string]bool{}
	for _, s := range Subscriptions(env) {
		following[s.ID] = true
	}

	machine := env
	machine.Session = ""
	nameOf := map[string]string{}
	for _, s := range Subscriptions(machine) {
		nameOf[s.ID] = s.Name
	}

	var joined []string
	for _, p := range projects {
		for _, id := range append([]string{p.Channel}, p.Channels...) {
			id = strings.TrimSpace(id)
			if id == "" || following[id] || !looksLikeID(id) {
				continue // a name is never resolved: only a namespace id is joined
			}
			if len(joined) == maxStartJoins {
				logLine(env, "project-join", "project "+p.ID+": "+id+": not joined, this start already joined "+strconv.Itoa(maxStartJoins))
				continue
			}
			following[id] = true

			name := id
			if known, ok := nameOf[id]; ok {
				name = known
				if st, ok := readSession(env, name); ok && !st.Joined {
					continue // left by hand in this session: it stays left
				}
			}

			if err := Join(ctx, env, name, "full", "all", "", io.Discard); err != nil {
				logLine(env, "project-join", "project "+p.ID+": "+name+": "+err.Error())
				continue
			}

			logLine(env, "project-join", "project "+p.ID+": joined "+name)
			joined = append(joined, name)
		}
	}

	if len(joined) > 0 && !markerSet(disarmedPath(env)) {
		if err := Arm(env); err != nil {
			logLine(env, "project-join", "arm: "+err.Error())
		}
	}

	return joined
}
