package axbackend

import (
	"context"
	"errors"
	"sync"
)

// Fake is an in-memory Backend for tests.
type Fake struct {
	mu        sync.Mutex
	created   []TaskSpec
	resumed   []string
	deleted   []string
	active    int
	maxActive int
	specs     map[string]TaskSpec

	CreateErr error
	ResumeErr error
	// OnGet decides the status of a polled task. It may block (honour ctx).
	// Nil means: done, succeeded, output "ok".
	OnGet func(ctx context.Context, s TaskSpec) Status
}

func NewFake() *Fake { return &Fake{specs: map[string]TaskSpec{}} }

func (f *Fake) CreateTask(_ context.Context, s TaskSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.created = append(f.created, s)
	f.specs[s.Name] = s
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	return nil
}

func (f *Fake) ResumeTask(_ context.Context, _, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, name)
	return f.ResumeErr
}

func (f *Fake) GetTask(ctx context.Context, _, name string) (Status, error) {
	f.mu.Lock()
	s, ok := f.specs[name]
	on := f.OnGet
	f.mu.Unlock()
	if !ok {
		return Status{}, errors.New("not found")
	}
	if on == nil {
		return Status{Done: true, Succeeded: true, Output: "ok"}, nil
	}
	return on(ctx, s), nil
}

func (f *Fake) DeleteTask(_ context.Context, _, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, name)
	if _, ok := f.specs[name]; ok {
		delete(f.specs, name)
		f.active--
	}
	return nil
}

func (f *Fake) Created() []TaskSpec { f.mu.Lock(); defer f.mu.Unlock(); return append([]TaskSpec(nil), f.created...) }
func (f *Fake) Resumed() []string   { f.mu.Lock(); defer f.mu.Unlock(); return append([]string(nil), f.resumed...) }
func (f *Fake) Deleted() []string   { f.mu.Lock(); defer f.mu.Unlock(); return append([]string(nil), f.deleted...) }
func (f *Fake) MaxActive() int      { f.mu.Lock(); defer f.mu.Unlock(); return f.maxActive }
func (f *Fake) Active() int         { f.mu.Lock(); defer f.mu.Unlock(); return f.active }
