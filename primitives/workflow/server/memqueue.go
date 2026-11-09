package server

import (
	"context"
	"sync"
)

// MemQueue is a Queue in memory, for tests without Corndogs. It is not
// for a service: its tasks are lost when the process stops.
type MemQueue struct {
	mu    sync.Mutex
	tasks []*memTask
}

type memTask struct {
	uuid, queue, key, state string
	rev                     int64
}

func (m *MemQueue) find(f func(*memTask) bool) *memTask {
	for _, t := range m.tasks {
		if f(t) {
			return t
		}
	}
	return nil
}

func (m *MemQueue) Submit(_ context.Context, queue, key string) (Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.find(func(t *memTask) bool { return t.queue == queue && t.key == key })
	if t == nil {
		t = &memTask{uuid: newID("mem_"), queue: queue, key: key, state: statePending, rev: 1}
		m.tasks = append(m.tasks, t)
	}
	return Ticket{UUID: t.uuid, Revision: t.rev}, nil
}

func (m *MemQueue) Claim(_ context.Context, queue string) (*Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.find(func(t *memTask) bool { return t.queue == queue && t.state == statePending })
	if t == nil {
		return nil, nil
	}
	t.state, t.rev = stateWorking, t.rev+1
	return &Delivery{Ticket: Ticket{UUID: t.uuid, Revision: t.rev}, Key: t.key}, nil
}

func (m *MemQueue) Finish(_ context.Context, _ string, tk Ticket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.find(func(t *memTask) bool { return t.uuid == tk.UUID }); t != nil {
		t.state = "completed"
	}
	return nil
}

func (m *MemQueue) Release(_ context.Context, _ string, tk Ticket) (Ticket, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.find(func(t *memTask) bool { return t.uuid == tk.UUID })
	if t == nil || t.state != stateWorking {
		return tk, false, nil
	}
	t.state, t.rev = statePending, t.rev+1
	return Ticket{UUID: t.uuid, Revision: t.rev}, true, nil
}
