package subagentbridge

import (
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// Silence watchdog.
//
// A subagent Pi process that loses its model connection (e.g. the proxy drops
// the socket into CLOSE_WAIT) stops producing ANY output while the run freezes:
// RPC mode streams every AgentSessionEvent - including model thinking/reply
// chunks - to stdout as they occur, so "no event for N minutes" reliably means
// "the child produced no output of any kind (not even streaming thinking) for
// N minutes", which a healthy subagent never does. No CPU sampling or tool
// tracking is needed; a slow-but-healthy model keeps emitting events, a wedged
// one goes completely silent (the incident that motivated this showed exactly
// that: zero events from the moment the model connection died).
//
// When the rule fires the whole child process tree is killed first (on Windows
// the direct child is the pi.cmd cmd.exe shim with node as a descendant, so a
// plain Kill would leave the wedged node orphaned and burning CPU), then the
// run context is cancelled. The existing runCtx.Done() path in handle() marks
// the run failed with this reason, persists a terminal record, and the parent
// agent receives the usual completion follow-up - so a wedged subagent can
// never freeze the parent session.

type wedgeParams struct {
	Tick       time.Duration // silence check interval
	SilenceMax time.Duration // abort when no event for at least this long
}

const maxWedgeSilence = time.Duration(math.MaxInt64)

func defaultWedgeParams() wedgeParams {
	return wedgeParams{
		Tick:       10 * time.Second,
		SilenceMax: 30 * time.Minute,
	}
}

// loadWedgeParams applies the optional environment override. The default 20
// minutes allows legitimate long-running tools (for example, asset generation)
// to remain silent while still bounding a genuinely wedged child process.
// Operators may raise it per deployment (e.g. a value at or above the duration
// limit is clamped to effectively disable it).
func loadWedgeParams() wedgeParams {
	p := defaultWedgeParams()
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("CODINGTO_SUBAGENT_SILENT_KILL_MS")), 64); err == nil && v > 0 {
		maxMilliseconds := float64(maxWedgeSilence / time.Millisecond)
		if v >= maxMilliseconds {
			p.SilenceMax = maxWedgeSilence
		} else {
			p.SilenceMax = time.Duration(v * float64(time.Millisecond))
		}
	}
	return p
}

// decide reports the abort reason when the run has been silent (no events)
// for at least SilenceMax, otherwise "". Any received event - including model
// thinking/streaming chunks - resets the silence window via lastEventAt.
func (p wedgeParams) decide(now, lastEventAt time.Time) string {
	silence := now.Sub(lastEventAt)
	if silence < 0 {
		silence = 0
	}
	if silence >= p.SilenceMax {
		return "subagent silent for " + silence.Round(time.Second).String() +
			" (no events, no model output); auto-aborted"
	}
	return ""
}
