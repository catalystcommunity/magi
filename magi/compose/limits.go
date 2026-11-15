package compose

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/catalystcommunity/magi/lib/rpc"
)

// Limits are the per-user rate limits and quotas on model calls and tasks.
// A value of 0 turns that limit off. Windows are fixed: the count of a
// user starts again at the end of the window that holds its first call.
type Limits struct {
	ModelPerMinute, ModelPerDay int // model calls of one user
	TasksPerHour, TasksPerDay   int // code-change runs started for one user
}

// DefaultLimits are generous: a household does not reach them, but a
// loop or a stolen token cannot spend without bound.
func DefaultLimits() Limits {
	return Limits{ModelPerMinute: 120, ModelPerDay: 5000, TasksPerHour: 20, TasksPerDay: 100}
}

// limitsFromEnv reads MAGI_LIMIT_MODEL_PER_MINUTE, MAGI_LIMIT_MODEL_PER_DAY,
// MAGI_LIMIT_TASKS_PER_HOUR and MAGI_LIMIT_TASKS_PER_DAY. 0 turns a limit off.
func limitsFromEnv() (Limits, error) {
	l := DefaultLimits()
	for _, f := range []struct {
		name string
		to   *int
	}{
		{"MAGI_LIMIT_MODEL_PER_MINUTE", &l.ModelPerMinute}, {"MAGI_LIMIT_MODEL_PER_DAY", &l.ModelPerDay},
		{"MAGI_LIMIT_TASKS_PER_HOUR", &l.TasksPerHour}, {"MAGI_LIMIT_TASKS_PER_DAY", &l.TasksPerDay},
	} {
		if v := os.Getenv(f.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return l, fmt.Errorf("%s: want a whole number of 0 or more (0 turns the limit off), got %q", f.name, v)
			}
			*f.to = n
		}
	}
	return l, nil
}

type limitKey struct{ kind, user string }

type window struct {
	start time.Time
	n     int
}

// limiter counts calls per user and kind. The state is in memory: Magi
// runs as one replica (deploy/render.py), and a restart gives each user a
// fresh window, which is acceptable for a bound on spend and abuse.
type limiter struct {
	mu  sync.Mutex
	now func() time.Time
	log *slog.Logger
	w   map[limitKey]map[time.Duration]*window
}

func newLimiter(log *slog.Logger) *limiter {
	return &limiter{now: time.Now, log: log, w: map[limitKey]map[time.Duration]*window{}}
}

type limitRule struct {
	per time.Duration
	max int
	cap string // "per minute"
}

// take counts one call of kind for user against rules. It counts nothing
// and returns an error when any rule is at its maximum.
func (l *limiter) take(kind, user string, rules []limitRule) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.w) > 1024 {
		l.prune(now)
	}
	k := limitKey{kind, user}
	ws := l.w[k]
	if ws == nil {
		ws = map[time.Duration]*window{}
	}
	for _, r := range rules {
		if r.max <= 0 {
			continue
		}
		w := ws[r.per]
		if w == nil || !now.Before(w.start.Add(r.per)) {
			w = &window{start: now}
			ws[r.per] = w
		}
		if w.n >= r.max {
			retry := w.start.Add(r.per).Sub(now).Round(time.Second)
			if retry < time.Second {
				retry = time.Second
			}
			if l.log != nil {
				l.log.Warn("limit reached", "user", user, "kind", kind, "limit", fmt.Sprintf("%d %s", r.max, r.cap), "retry_after", retry.String())
			}
			return rpc.Errorf(http.StatusTooManyRequests, "limit reached: at most %d %s %s for one person. Try again in %s.",
				r.max, kindName(kind), r.cap, retry)
		}
	}
	for _, r := range rules {
		if r.max > 0 {
			ws[r.per].n++
		}
	}
	l.w[k] = ws
	return nil
}

func kindName(kind string) string {
	if kind == "task" {
		return "task starts"
	}
	return "model calls"
}

func (l *limiter) prune(now time.Time) {
	for k, ws := range l.w {
		for per, w := range ws {
			if !now.Before(w.start.Add(per)) {
				delete(ws, per)
			}
		}
		if len(ws) == 0 {
			delete(l.w, k)
		}
	}
}

// allowModel counts one model call for user, the person that the call is
// for: the owner of the run for persona turns, the note taker and the egress
// review, the author's person for the librarian, and the owner of the
// run that a runner works on for the runner model proxy. It returns a 429
// error when the user is over a limit.
func (s *System) allowModel(user string) error {
	if s.limits == nil {
		return nil
	}
	l := s.Cfg.Limits
	return s.limits.take("model", user, []limitRule{{time.Minute, l.ModelPerMinute, "per minute"}, {24 * time.Hour, l.ModelPerDay, "per day"}})
}

// allowTask counts one started task for user.
func (s *System) allowTask(user string) error {
	if s.limits == nil {
		return nil
	}
	l := s.Cfg.Limits
	return s.limits.take("task", user, []limitRule{{time.Hour, l.TasksPerHour, "per hour"}, {24 * time.Hour, l.TasksPerDay, "per day"}})
}

// limitMessage is the message of a limit error, for the person.
func limitMessage(err error) string {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}

// isLimit reports whether err is a limit error.
func isLimit(err error) bool { return rpc.CodeOf(err) == http.StatusTooManyRequests }
