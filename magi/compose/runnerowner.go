package compose

import (
	"sync"
	"time"
)

// runnerOwners remembers the owner of the run that each runner claimed
// last. The runner model proxy gets only the runner token, so it uses this
// to charge the model calls of a runner to the person whose run it works
// on. A runner that works on several runs at once is charged to the owner
// of the newest claim.
type runnerOwners struct {
	mu sync.Mutex
	m  map[string]runnerOwner
}

type runnerOwner struct {
	owner string
	until time.Time
}

// runnerOwnerTTL bounds how long a claim stands for a runner that does not
// complete (a crashed runner): after it, its calls are charged to the runner.
const runnerOwnerTTL = 6 * time.Hour

func (r *runnerOwners) set(runner, owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]runnerOwner{}
	}
	r.m[runner] = runnerOwner{owner, time.Now().Add(runnerOwnerTTL)}
}

func (r *runnerOwners) clear(runner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, runner)
}

// of gives the user to charge for runner: the owner of its current run, or
// the runner itself ("runner:<name>") when it holds no run.
func (r *runnerOwners) of(runner string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o, ok := r.m[runner]; ok && time.Now().Before(o.until) && o.owner != "" {
		return o.owner
	}
	return "runner:" + runner
}
