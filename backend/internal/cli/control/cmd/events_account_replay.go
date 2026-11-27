package cmd

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

const accountRotationInterval = 2 * time.Second

type workerHistoryKey struct {
	id      string
	tabType leapmuxv1.TabType
}

// replaySourceState tracks admission, liveness, backlog high-water, and stop
// state for one source (either the Hub layout stream or one Worker family).
type replaySourceState struct {
	live      bool
	pending   bool
	uncovered bool
	admitted  bool
	high      int64
	stopped   *cli.Error
}

type accountWorkerWatch struct {
	replaySourceState
	key    workerHistoryKey
	cancel context.CancelFunc
}

type accountDelivery struct {
	apply func() (bool, error)
	saved chan struct{}
}

type accountReplay struct {
	session        *session
	ctx            context.Context
	opts           eventsOptions
	printer        *eventPrinter
	clock          quartz.Clock
	file           *eventCheckpointFile
	state          *eventCheckpoint
	dirty          bool
	kinds          []leapmuxv1.TabType
	workers        map[workerHistoryKey]*accountWorkerWatch
	workerChannels *sharedWorkerChannels
	hub            *layoutPageConsumer
	hubState       replaySourceState
	hubExpected    bool
	delivery       chan accountDelivery
	readySignal    chan struct{}
	workerSlots    chan struct{}
	slotBudget     int
	activeWorkers  atomic.Int64
	rotationOnce   sync.Once
	rotationMode   chan struct{}
	pending        int
	ready          bool
	sources        sync.WaitGroup
}

func accountWorkerKinds(filter kindFilter) []leapmuxv1.TabType {
	var kinds []leapmuxv1.TabType
	for _, tabType := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		if len(eventCheckpointKinds(filter, tabType)) != 0 {
			kinds = append(kinds, tabType)
		}
	}
	return kinds
}

func accountFactKinds(filter kindFilter) []string {
	return filter.retainedKinds(replayHubLayout, replayWorkerAgent, replayWorkerTerminal)
}

func (s *session) runAccountCheckpointedEvents(parentCtx context.Context, clock quartz.Clock, opts eventsOptions) error {
	file, saved, err := openCheckedEventCheckpoint(opts.checkpoint, accountCheckpointScope)
	if err != nil {
		return err
	}
	defer file.close()
	filter, err := validateAccountCheckpointFilter(opts.filter)
	if err != nil {
		return err
	}
	if err := verifyCheckpointFilter(saved, filter); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parentCtx)
	r := &accountReplay{
		session:        s,
		ctx:            ctx,
		opts:           opts,
		printer:        newEventPrinter(s.inv.Printer, opts.filter, opts.limit),
		clock:          clock,
		file:           file,
		state:          saved,
		kinds:          accountWorkerKinds(opts.filter),
		workers:        make(map[workerHistoryKey]*accountWorkerWatch),
		workerChannels: newSharedWorkerChannels(s.c),
		delivery:       make(chan accountDelivery),
		readySignal:    make(chan struct{}),
		workerSlots:    make(chan struct{}, detailFanoutLimit),
		slotBudget:     detailFanoutLimit,
		rotationMode:   make(chan struct{}),
	}
	defer func() {
		cancel()
		r.sources.Wait()
	}()

	inventory, err := r.initialInventory()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	fresh := saved == nil
	if fresh {
		r.state = &eventCheckpoint{
			Version: eventCheckpointVersion, Scope: accountCheckpointScope, Filter: filter,
			Account: &accountEventCheckpoint{OwnerID: inventory.owner, Workers: make(map[string]accountWorkerPositions)},
		}
	}
	if r.state.Account.OwnerID != inventory.owner {
		return cli.Newf(cli.CodeConflict, "the event checkpoint belongs to a different account")
	}
	if err := r.startHub(fresh); err != nil {
		return err
	}
	if err := r.applyInventory(inventory, fresh); err != nil {
		return err
	}
	return r.run()
}

func (r *accountReplay) nowClock() quartz.Clock {
	if r.clock == nil {
		r.clock = quartz.NewReal()
	}
	return r.clock
}

func (r *accountReplay) effectiveSlotBudget() int {
	if r.slotBudget <= 0 {
		return detailFanoutLimit
	}
	return r.slotBudget
}

func (r *accountReplay) enterRotationMode() {
	if r.rotationMode == nil {
		return
	}
	r.rotationOnce.Do(func() {
		close(r.rotationMode)
	})
}

func (r *accountReplay) isRotating() bool {
	if r.rotationMode == nil {
		return false
	}
	select {
	case <-r.rotationMode:
		return true
	default:
		return false
	}
}

func (r *accountReplay) saveStateNow() error {
	if err := r.file.save(r.state); err != nil {
		return err
	}
	r.dirty = false
	return nil
}

func (r *accountReplay) flush() error {
	if r.hub != nil {
		if err := r.hub.flush(); err != nil {
			return err
		}
	}
	if !r.dirty || r.state == nil {
		return nil
	}
	return r.saveStateNow()
}

func (r *accountReplay) run() error {
	var ticks <-chan time.Time
	if r.opts.heartbeat > 0 {
		timer := r.nowClock().NewTicker(r.opts.heartbeat, "events-account-heartbeat")
		defer timer.Stop("events-account-heartbeat")
		ticks = timer.C
	}
	for {
		if failure := r.allSourcesStopped(); failure != nil {
			_ = r.flush()
			return failure
		}
		if !r.ready && r.pending == 0 {
			if err := r.announceReady(); err != nil {
				return err
			}
			if r.opts.snapshot && r.allBacklogCaughtUp() {
				return r.flush()
			}
		}
		select {
		case <-r.ctx.Done():
			return r.flush()
		case incoming := <-r.delivery:
			done, err := incoming.apply()
			if err != nil {
				return err
			}
			close(incoming.saved)
			if done || (r.opts.snapshot && r.ready && r.allBacklogCaughtUp()) {
				return r.flush()
			}
		case <-ticks:
			if r.ready {
				event := r.notice(kindStreamHeartbeat, "watching account event history")
				event.Scope = r.scope()
				if _, err := r.printer.printCheckpointed(event); err != nil {
					return err
				}
			}
		}
	}
}

// allBacklogCaughtUp reports whether every active source in this account replay
// has caught up to the high-water sequence announced at admission.
func (r *accountReplay) allBacklogCaughtUp() bool {
	if r.hubExpected && r.hubState.stopped == nil {
		if !r.hubState.live || r.hub == nil || !r.hub.caughtUp() {
			return false
		}
	}
	for key, watch := range r.workers {
		if watch.stopped != nil {
			continue
		}
		if !watch.live {
			return false
		}
		saved := r.workerPosition(key)
		if saved == nil || saved.Sequence < watch.high {
			return false
		}
	}
	return true
}

// deliver waits for output and checkpointing before the source advances.
func (r *accountReplay) deliver(ctx context.Context, apply func() (bool, error)) error {
	item := accountDelivery{apply: apply, saved: make(chan struct{})}
	select {
	case r.delivery <- item:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-item.saved:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Invalid frames stop their source. Output and save errors stop the command.
func (r *accountReplay) deliverSource(ctx context.Context, invalid error, apply func() (bool, error)) error {
	var sourceFailure error
	err := r.deliver(ctx, func() (bool, error) {
		done, err := apply()
		if errors.Is(err, invalid) {
			sourceFailure = err
			return false, nil
		}
		return done, err
	})
	if err != nil {
		return err
	}
	return sourceFailure
}

func (r *accountReplay) notice(kind, summary string) streamEvent {
	return streamEvent{Kind: kind, Time: r.nowClock().Now().UTC(), Summary: summary}
}

func (r *accountReplay) printNotice(event streamEvent) error {
	_, err := r.printer.printCheckpointed(event)
	return err
}

func (r *accountReplay) scope() *scopeCounts {
	live := make(map[string]bool)
	for _, watch := range r.workers {
		if watch.live {
			live[watch.key.id] = true
		}
	}
	return &scopeCounts{Workers: len(r.state.Account.Workers), WorkersLive: len(live), HubLive: r.hubState.live}
}

func (r *accountReplay) allSourcesStopped() *cli.Error {
	var stopped *cli.Error
	if r.hubExpected {
		if r.hubState.stopped == nil {
			return nil
		}
		stopped = r.hubState.stopped
	}
	keys := slices.Collect(maps.Keys(r.workers))
	slices.SortFunc(keys, func(a, b workerHistoryKey) int {
		if c := cmp.Compare(a.id, b.id); c != 0 {
			return c
		}
		return cmp.Compare(resolve.TabTypeWireName(a.tabType), resolve.TabTypeWireName(b.tabType))
	})
	for _, key := range keys {
		watch := r.workers[key]
		if watch.stopped == nil {
			return nil
		}
		if stopped == nil {
			stopped = watch.stopped
		}
	}
	return stopped
}

func (r *accountReplay) announceReady() error {
	event := r.notice(kindStreamReady, "watching account event history")
	event.Scope = r.scope()
	if err := r.printNotice(event); err != nil {
		return err
	}
	r.ready = true
	close(r.readySignal)
	return nil
}

type accountInventory struct {
	owner     string
	revision  int64
	workerIDs []string
}

func readAccountInventory(ctx context.Context, s *session) (accountInventory, error) {
	ctx, cancel := rpcDeadline(ctx)
	defer cancel()
	resp := new(leapmuxv1.GetWorkerInventoryResponse)
	if err := hubCallUnary(ctx, s.c, leapmuxv1connect.WorkerManagementServiceGetWorkerInventoryProcedure,
		&leapmuxv1.GetWorkerInventoryRequest{}, resp); err != nil {
		return accountInventory{}, err
	}
	if resp.GetOwnerUserId() == "" || resp.GetRevision() < 0 {
		return accountInventory{}, cli.Newf(cli.CodeInternal, "the Hub returned an invalid Worker inventory position")
	}
	workerIDs := make([]string, 0, len(resp.GetWorkerIds()))
	for _, workerID := range resp.GetWorkerIds() {
		if workerID == "" {
			return accountInventory{}, cli.Newf(cli.CodeInternal, "the Hub returned an empty Worker ID")
		}
		workerIDs = append(workerIDs, workerID)
	}
	slices.Sort(workerIDs)
	return accountInventory{
		owner:     resp.GetOwnerUserId(),
		revision:  resp.GetRevision(),
		workerIDs: workerIDs,
	}, nil
}

func (r *accountReplay) initialInventory() (accountInventory, error) {
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	for {
		inventory, err := readAccountInventory(r.ctx, r.session)
		if err == nil || r.ctx.Err() != nil {
			return inventory, err
		}
		if r.opts.noReconnect || !cli.FromError(err).Retryable() || !waitReplayRetry(r.ctx, r.nowClock(), backoff.afterSession(false)) {
			return inventory, err
		}
	}
}

func (r *accountReplay) applyInventory(inventory accountInventory, fresh bool) error {
	account := r.state.Account
	if inventory.owner != account.OwnerID {
		return cli.Newf(cli.CodeConflict, "the live Worker inventory belongs to a different account")
	}
	current := make(map[string]bool, len(inventory.workerIDs))
	changed := account.EnrollmentRevision != inventory.revision || len(account.Workers) != len(inventory.workerIDs)
	for _, id := range inventory.workerIDs {
		current[id] = true
		changed = changed || account.Workers[id] == nil
	}
	if !fresh && changed {
		event := r.notice(kindStreamGap, "Worker membership changed since the saved inventory")
		event.Source, event.GapReason = "worker", "worker_membership"
		for _, tabType := range r.kinds {
			event.Lost = append(event.Lost, eventCheckpointKinds(r.opts.filter, tabType)...)
		}
		if err := r.printNotice(event); err != nil {
			return err
		}
	}
	for key, watch := range r.workers {
		if !current[key.id] {
			r.markSourceResponded(&watch.replaySourceState)
			watch.cancel()
			delete(r.workers, key)
		}
	}
	for id := range account.Workers {
		if !current[id] {
			delete(account.Workers, id)
		}
	}
	for _, id := range inventory.workerIDs {
		if account.Workers[id] == nil {
			account.Workers[id] = make(accountWorkerPositions)
		}
	}
	r.activeWorkers.Store(int64(len(inventory.workerIDs)))
	if len(inventory.workerIDs) > r.effectiveSlotBudget() {
		r.enterRotationMode()
	}
	account.EnrollmentRevision = inventory.revision
	if fresh || changed {
		if err := r.saveStateNow(); err != nil {
			return err
		}
	}
	for _, id := range inventory.workerIDs {
		for _, tabType := range r.kinds {
			key := workerHistoryKey{id: id, tabType: tabType}
			if r.workers[key] == nil {
				r.startWorker(key, !fresh)
			}
		}
	}
	return nil
}

func (r *accountReplay) markSourceResponded(src *replaySourceState) {
	if src.pending {
		src.pending = false
		r.pending--
	}
}
