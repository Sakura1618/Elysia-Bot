package runtimecore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	eventmodel "github.com/ohmyopencode/bot-platform/packages/event-model"
	cronlib "github.com/robfig/cron/v3"
)

type ScheduleKind string

const (
	ScheduleKindCron    ScheduleKind = "cron"
	ScheduleKindDelay   ScheduleKind = "delay"
	ScheduleKindOneShot ScheduleKind = "one-shot"
)

type SchedulePlan struct {
	ID        string
	Kind      ScheduleKind
	CronExpr  string
	Delay     time.Duration
	ExecuteAt time.Time
	Source    string
	EventType string
	Metadata  map[string]any
}

type Scheduler struct {
	mu           sync.RWMutex
	plans        map[string]SchedulePlan
	dueAt        map[string]time.Time
	now          func() time.Time
	running      bool
	store        schedulerStore
	logger       *Logger
	tracer       *TraceRecorder
	metrics      *MetricsRegistry
	lastRecovery ScheduleRecoverySnapshot
}

type ScheduleRecoverySnapshot struct {
	RecoveredAt        time.Time            `json:"recoveredAt"`
	TotalSchedules     int                  `json:"totalSchedules"`
	RecoveredSchedules int                  `json:"recoveredSchedules"`
	InvalidSchedules   int                  `json:"invalidSchedules"`
	ScheduleKinds      map[ScheduleKind]int `json:"scheduleKinds,omitempty"`
}

type schedulerStore interface {
	SaveSchedulePlan(context.Context, storedSchedulePlan) error
	LoadSchedulePlan(context.Context, string) (storedSchedulePlan, error)
	ListSchedulePlans(context.Context) ([]storedSchedulePlan, error)
	DeleteSchedulePlan(context.Context, string) error
}

var standardCronParser = cronlib.NewParser(cronlib.Minute | cronlib.Hour | cronlib.Dom | cronlib.Month | cronlib.Dow)

func NewScheduler() *Scheduler {
	return &Scheduler{
		plans: make(map[string]SchedulePlan),
		dueAt: make(map[string]time.Time),
		now: func() time.Time {
			return time.Now().UTC()
		},
		logger:  NewLogger(io.Discard),
		tracer:  NewTraceRecorder(),
		metrics: NewMetricsRegistry(),
	}
}

func (s *Scheduler) SetObservability(logger *Logger, tracer *TraceRecorder, metrics *MetricsRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if logger != nil {
		s.logger = logger
	}
	if tracer != nil {
		s.tracer = tracer
	}
	if metrics != nil {
		s.metrics = metrics
	}
}

func (s *Scheduler) SetStore(store schedulerStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}

func (s *Scheduler) Register(plan SchedulePlan) error {
	s.mu.Lock()

	if err := plan.Validate(); err != nil {
		s.mu.Unlock()
		return err
	}
	if _, exists := s.plans[plan.ID]; exists {
		s.mu.Unlock()
		return fmt.Errorf("schedule %q already exists", plan.ID)
	}
	if s.store != nil {
		if _, err := s.store.LoadSchedulePlan(context.Background(), plan.ID); err == nil {
			s.mu.Unlock()
			return fmt.Errorf("schedule %q already exists", plan.ID)
		} else if !errors.Is(err, sql.ErrNoRows) {
			s.mu.Unlock()
			return err
		}
	}
	dueAt, err := s.dueAtForPlan(plan)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.savePlanLocked(plan, dueAt); err != nil {
		s.mu.Unlock()
		return err
	}
	s.plans[plan.ID] = plan
	s.dueAt[plan.ID] = dueAt
	s.mu.Unlock()
	return nil
}

func (s *Scheduler) Plan(id string) (SchedulePlan, error) {
	if s.store != nil {
		stored, err := s.store.LoadSchedulePlan(context.Background(), id)
		if err == nil {
			return stored.Plan, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return SchedulePlan{}, err
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	plan, exists := s.plans[id]
	if !exists {
		return SchedulePlan{}, errors.New("schedule not found")
	}
	return plan, nil
}

func (s *Scheduler) Cancel(id string) error {
	s.mu.Lock()
	if s.store != nil {
		if _, err := s.store.LoadSchedulePlan(context.Background(), id); err != nil {
			s.mu.Unlock()
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("schedule not found")
			}
			return err
		}
		if err := s.store.DeleteSchedulePlan(context.Background(), id); err != nil {
			s.mu.Unlock()
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("schedule not found")
			}
			return err
		}
	} else if _, exists := s.plans[id]; !exists {
		s.mu.Unlock()
		return errors.New("schedule not found")
	}
	delete(s.plans, id)
	delete(s.dueAt, id)
	s.mu.Unlock()
	return nil
}

func (s *Scheduler) Plans() []SchedulePlan {
	if s.store != nil {
		storedPlans, err := s.store.ListSchedulePlans(context.Background())
		if err == nil {
			plans := make([]SchedulePlan, 0, len(storedPlans))
			for _, stored := range storedPlans {
				plans = append(plans, stored.Plan)
			}
			return plans
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	plans := make([]SchedulePlan, 0, len(s.plans))
	for _, plan := range s.plans {
		plans = append(plans, plan)
	}
	return plans
}

func (s *Scheduler) Start(ctx context.Context, interval time.Duration, dispatch func(eventmodel.Event) error) error {
	if interval <= 0 {
		return errors.New("scheduler interval must be > 0")
	}
	if dispatch == nil {
		return errors.New("scheduler dispatch callback is required")
	}
	if err := s.Restore(ctx); err != nil {
		return err
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("scheduler runner already started")
	}
	s.running = true
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runDuePlans(ctx, dispatch)
			}
		}
	}()

	return nil
}

func (s *Scheduler) Restore(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	storedPlans, err := s.store.ListSchedulePlans(ctx)
	if err != nil {
		return err
	}

	recovery := ScheduleRecoverySnapshot{
		RecoveredAt:   s.now(),
		ScheduleKinds: map[ScheduleKind]int{},
	}
	restoredPlans := make(map[string]SchedulePlan, len(storedPlans))
	restoredDueAt := make(map[string]time.Time, len(storedPlans))
	for _, stored := range storedPlans {
		recovery.TotalSchedules++
		if err := stored.Plan.Validate(); err != nil {
			recovery.InvalidSchedules++
			continue
		}
		missingDueAt := stored.DueAt == nil || stored.DueAt.IsZero()
		dueAt, err := restoredScheduleDueAt(s.now, stored)
		if err != nil {
			recovery.InvalidSchedules++
			continue
		}
		if dueAt == nil || dueAt.IsZero() {
			computedDueAt, err := s.dueAtForPlan(stored.Plan)
			if err != nil {
				recovery.InvalidSchedules++
				continue
			}
			dueAt = &computedDueAt
			missingDueAt = true
		}
		if missingDueAt {
			if err := s.persistRestoredPlan(ctx, stored, *dueAt); err != nil {
				return err
			}
			recovery.RecoveredSchedules++
			if s.metrics != nil {
				s.metrics.IncrementScheduleRecoveries()
			}
		}
		restoredPlans[stored.Plan.ID] = stored.Plan
		restoredDueAt[stored.Plan.ID] = *dueAt
		recovery.ScheduleKinds[stored.Plan.Kind]++
	}

	s.mu.Lock()
	s.plans = restoredPlans
	s.dueAt = restoredDueAt
	s.lastRecovery = recovery
	s.mu.Unlock()
	if s.logger != nil {
		_ = s.logger.Log("info", "scheduler restored from persistence", LogContext{}, map[string]any{
			"restored_schedules":       recovery.TotalSchedules,
			"recovered_schedules":      recovery.RecoveredSchedules,
			"invalid_schedules":        recovery.InvalidSchedules,
			"persisted_schedule_kinds": recovery.ScheduleKinds,
		})
	}
	return nil
}

func (s *Scheduler) persistRestoredPlan(ctx context.Context, stored storedSchedulePlan, dueAt time.Time) error {
	if s.store == nil {
		return nil
	}
	repairedDueAt := dueAt
	return s.store.SaveSchedulePlan(ctx, storedSchedulePlan{
		Plan:          stored.Plan,
		DueAt:         &repairedDueAt,
		DueAtEvidence: scheduleDueAtEvidenceRecoveredStartup,
		CreatedAt:     stored.CreatedAt,
		UpdatedAt:     s.now(),
	})
}

func (s *Scheduler) LastRecoverySnapshot() ScheduleRecoverySnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cloned := s.lastRecovery
	if len(s.lastRecovery.ScheduleKinds) > 0 {
		cloned.ScheduleKinds = make(map[ScheduleKind]int, len(s.lastRecovery.ScheduleKinds))
		for kind, count := range s.lastRecovery.ScheduleKinds {
			cloned.ScheduleKinds[kind] = count
		}
	}
	return cloned
}

func restoredScheduleDueAt(now func() time.Time, stored storedSchedulePlan) (*time.Time, error) {
	if stored.DueAt != nil && !stored.DueAt.IsZero() {
		return stored.DueAt, nil
	}
	switch stored.Plan.Kind {
	case ScheduleKindDelay:
		if stored.CreatedAt.IsZero() {
			return nil, nil
		}
		dueAt := stored.CreatedAt.Add(stored.Plan.Delay)
		return &dueAt, nil
	case ScheduleKindOneShot:
		if stored.Plan.ExecuteAt.IsZero() {
			return nil, nil
		}
		dueAt := stored.Plan.ExecuteAt
		return &dueAt, nil
	case ScheduleKindCron:
		if now == nil {
			return nil, nil
		}
		dueAt, err := nextCronDue(stored.Plan.CronExpr, now(), now())
		if err != nil {
			return nil, err
		}
		return &dueAt, nil
	default:
		return nil, fmt.Errorf("unsupported schedule kind %q", stored.Plan.Kind)
	}
}

func (s *Scheduler) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *Scheduler) Trigger(id string) (eventmodel.Event, error) {
	plan, err := s.Plan(id)
	if err != nil {
		return eventmodel.Event{}, err
	}
	return s.eventForPlan(id, plan)
}

func (s *Scheduler) eventForPlan(id string, plan SchedulePlan) (eventmodel.Event, error) {
	now := s.now()
	event := eventmodel.Event{
		EventID:        fmt.Sprintf("evt-schedule-%s", id),
		TraceID:        fmt.Sprintf("trace-schedule-%s", id),
		Source:         plan.Source,
		Type:           plan.EventType,
		Timestamp:      now,
		System:         &eventmodel.SystemEvent{Name: plan.ID, Status: "triggered"},
		Metadata:       map[string]any{"schedule_kind": string(plan.Kind)},
		IdempotencyKey: fmt.Sprintf("schedule:%s:%s", id, now.Format(time.RFC3339Nano)),
	}
	for key, value := range plan.Metadata {
		event.Metadata[key] = value
	}
	return event, event.Validate()
}

func (s *Scheduler) runDuePlans(ctx context.Context, dispatch func(eventmodel.Event) error) {
	now := s.now()
	type duePlan struct {
		id    string
		plan  SchedulePlan
		dueAt time.Time
	}
	due := make([]duePlan, 0)

	s.mu.RLock()
	for id, plan := range s.plans {
		dueAt, ok := s.dueAt[id]
		if !ok || dueAt.After(now) {
			continue
		}
		due = append(due, duePlan{id: id, plan: plan, dueAt: dueAt})
	}
	s.mu.RUnlock()

	for _, item := range due {
		select {
		case <-ctx.Done():
			return
		default:
		}

		event, err := s.eventForPlan(item.id, item.plan)
		if err != nil {
			continue
		}
		if err := dispatch(event); err != nil {
			continue
		}

		s.mu.Lock()
		plan, exists := s.plans[item.id]
		if !exists {
			s.mu.Unlock()
			continue
		}
		switch plan.Kind {
		case ScheduleKindCron:
			nextDueAt, err := nextCronDue(plan.CronExpr, item.dueAt, now)
			if err != nil {
				if err := s.deletePlanLocked(item.id); err != nil {
					s.mu.Unlock()
					continue
				}
				delete(s.plans, item.id)
				delete(s.dueAt, item.id)
				s.mu.Unlock()
				continue
			}
			if err := s.savePlanLocked(plan, nextDueAt); err != nil {
				s.mu.Unlock()
				continue
			}
			s.dueAt[item.id] = nextDueAt
		default:
			if err := s.deletePlanLocked(item.id); err != nil {
				s.mu.Unlock()
				continue
			}
			delete(s.plans, item.id)
			delete(s.dueAt, item.id)
		}
		s.mu.Unlock()
	}
}

func (s *Scheduler) setDueAtLocked(plan SchedulePlan) {
	dueAt, err := s.dueAtForPlan(plan)
	if err != nil {
		delete(s.dueAt, plan.ID)
		return
	}
	s.dueAt[plan.ID] = dueAt
}

func (s *Scheduler) dueAtForPlan(plan SchedulePlan) (time.Time, error) {
	switch plan.Kind {
	case ScheduleKindCron:
		return nextCronDue(plan.CronExpr, s.now(), s.now())
	case ScheduleKindDelay:
		return s.now().Add(plan.Delay), nil
	case ScheduleKindOneShot:
		return plan.ExecuteAt, nil
	default:
		return time.Time{}, fmt.Errorf("unsupported schedule kind %q", plan.Kind)
	}
}

func (s *Scheduler) savePlanLocked(plan SchedulePlan, dueAt time.Time) error {
	if s.store == nil {
		return nil
	}
	now := s.now()
	createdAt := now
	if existing, err := s.store.LoadSchedulePlan(context.Background(), plan.ID); err == nil {
		createdAt = existing.CreatedAt
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.store.SaveSchedulePlan(context.Background(), storedSchedulePlan{
		Plan:          plan,
		DueAt:         &dueAt,
		DueAtEvidence: scheduleDueAtEvidencePersisted,
		CreatedAt:     createdAt,
		UpdatedAt:     now,
	})
}

func (s *Scheduler) deletePlanLocked(id string) error {
	if s.store == nil {
		return nil
	}
	return s.store.DeleteSchedulePlan(context.Background(), id)
}

func (p SchedulePlan) Validate() error {
	if p.ID == "" {
		return errors.New("schedule id is required")
	}
	if p.Source == "" {
		return errors.New("schedule source is required")
	}
	if p.EventType == "" {
		return errors.New("schedule event type is required")
	}

	switch p.Kind {
	case ScheduleKindCron:
		if p.CronExpr == "" {
			return errors.New("cron expression is required")
		}
		if _, err := parseCronSchedule(p.CronExpr); err != nil {
			return fmt.Errorf("invalid cron expression: %w", err)
		}
	case ScheduleKindDelay:
		if p.Delay <= 0 {
			return errors.New("delay must be > 0")
		}
	case ScheduleKindOneShot:
		if p.ExecuteAt.IsZero() {
			return errors.New("execute_at is required")
		}
	default:
		return fmt.Errorf("unsupported schedule kind %q", p.Kind)
	}

	return nil
}

func parseCronSchedule(expr string) (cronlib.Schedule, error) {
	return standardCronParser.Parse(expr)
}

func nextCronDue(expr string, after time.Time, notBefore time.Time) (time.Time, error) {
	schedule, err := parseCronSchedule(expr)
	if err != nil {
		return time.Time{}, err
	}
	next := schedule.Next(after.UTC())
	for !next.After(notBefore.UTC()) {
		next = schedule.Next(next)
	}
	return next, nil
}
