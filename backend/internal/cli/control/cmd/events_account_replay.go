package cmd

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

const accountInventoryInterval = 2 * time.Second

type workerHistoryKey struct {
	id      string
	tabType leapmuxv1.TabType
}

type accountWorkerWatch struct {
	key       workerHistoryKey
	cancel    context.CancelFunc
	high      int64
	uncovered bool
	pending   bool
	live      bool
	admitted  bool
	stopped   *cli.Error
}

type accountDelivery struct {
	apply func() (bool, error)
	saved chan struct{}
}

type accountReplay struct {
	session       *session
	ctx           context.Context
	opts          eventsOptions
	file          *eventCheckpointFile
	state         *eventCheckpoint
	kinds         []leapmuxv1.TabType
	workers       map[workerHistoryKey]*accountWorkerWatch
	hub           *layoutEventReplay
	hubLive       bool
	hubExpected   bool
	hubPending    bool
	hubUncovered  bool
	hubStopped    *cli.Error
	workerPrinted int
	delivery      chan accountDelivery
	readySignal   chan struct{}
	workerSlots   chan struct{}
	pending       int
	ready         bool
	sources       sync.WaitGroup
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
	kinds := make([]string, 0)
	for _, kind := range eventKinds {
		if kind.source != sourceStream && filter.selects(kind.name) {
			kinds = append(kinds, kind.name)
		}
	}
	return kinds
}

func (s *session) runAccountCheckpointedEvents(opts eventsOptions) error {
	file, err := openEventCheckpoint(opts.checkpoint)
	if err != nil {
		return err
	}
	defer file.close()
	saved, err := file.read()
	if err != nil {
		return err
	}
	filter := accountFactKinds(opts.filter)
	if saved != nil && saved.Scope != accountCheckpointScope {
		return cli.Newf(cli.CodeConflict, "the checkpoint belongs to a different event scope")
	}
	if saved != nil && !slices.Equal(filter, saved.Filter) {
		return cli.Newf(cli.CodeConflict, "the event checkpoint uses a different --kind selection")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	r := &accountReplay{
		session: s, ctx: ctx, opts: opts, file: file, state: saved,
		kinds: accountWorkerKinds(opts.filter), workers: make(map[workerHistoryKey]*accountWorkerWatch),
		delivery: make(chan accountDelivery), readySignal: make(chan struct{}),
		workerSlots: make(chan struct{}, detailFanoutLimit),
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
	r.sources.Go(r.watchInventory)
	return r.run()
}

func (r *accountReplay) run() error {
	var ticks <-chan time.Time
	if r.opts.heartbeat > 0 {
		timer := time.NewTicker(r.opts.heartbeat)
		defer timer.Stop()
		ticks = timer.C
	}
	for {
		if failure := r.allSourcesStopped(); failure != nil {
			return failure
		}
		if !r.ready && r.pending == 0 {
			if err := r.announceReady(); err != nil {
				return err
			}
			if r.opts.snapshot && r.allBacklogCaughtUp() {
				return nil
			}
		}
		select {
		case <-r.ctx.Done():
			return nil
		case incoming := <-r.delivery:
			done, err := incoming.apply()
			if err != nil {
				return err
			}
			close(incoming.saved)
			if done || (r.opts.snapshot && r.ready && r.allBacklogCaughtUp()) {
				return nil
			}
		case <-ticks:
			if r.ready {
				event := r.notice(kindStreamHeartbeat, "watching account event history")
				event.Scope = r.scope()
				if err := r.print(event); err != nil {
					return err
				}
			}
		}
	}
}

// allBacklogCaughtUp reports whether every active source in this account replay
// has caught up to the high-water sequence announced at admission.
func (r *accountReplay) allBacklogCaughtUp() bool {
	if r.hubExpected && r.hubStopped == nil {
		if !r.hubLive || r.hub == nil || r.hub.state == nil || r.hub.state.Sequence < r.hub.replayHigh {
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
	return streamEvent{Kind: kind, Time: time.Now().UTC(), Summary: summary}
}

func (r *accountReplay) print(event streamEvent) error {
	return printCheckpointEvent(r.session.inv.Printer, r.opts.filter, event)
}

func (r *accountReplay) scope() *scopeCounts {
	live := make(map[string]bool)
	for _, watch := range r.workers {
		if watch.live {
			live[watch.key.id] = true
		}
	}
	return &scopeCounts{Workers: len(r.state.Account.Workers), WorkersLive: len(live), HubLive: r.hubLive}
}

func (r *accountReplay) allSourcesStopped() *cli.Error {
	var stopped *cli.Error
	if r.hubExpected {
		if r.hubStopped == nil {
			return nil
		}
		stopped = r.hubStopped
	}
	for _, watch := range r.workers {
		if watch.stopped == nil {
			return nil
		}
		stopped = watch.stopped
	}
	return stopped
}

func (r *accountReplay) announceReady() error {
	event := r.notice(kindStreamReady, "watching account event history")
	event.Scope = r.scope()
	if err := r.print(event); err != nil {
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
	readRevision := func() (*leapmuxv1.GetWorkerEnrollmentRevisionResponse, error) {
		result := new(leapmuxv1.GetWorkerEnrollmentRevisionResponse)
		err := hubCallUnary(ctx, s.c, leapmuxv1connect.WorkerManagementServiceGetWorkerEnrollmentRevisionProcedure,
			&leapmuxv1.GetWorkerEnrollmentRevisionRequest{}, result)
		return result, err
	}
	before, err := readRevision()
	if err != nil {
		return accountInventory{}, err
	}
	workers, err := listAllWorkers(ctx, s.c)
	if err != nil {
		return accountInventory{}, err
	}
	after, err := readRevision()
	if err != nil {
		return accountInventory{}, err
	}
	if before.GetOwnerUserId() != after.GetOwnerUserId() || before.GetRevision() != after.GetRevision() ||
		before.GetActiveWorkers() != after.GetActiveWorkers() || int64(len(workers)) != after.GetActiveWorkers() {
		return accountInventory{}, cli.Newf(cli.CodeUnavailable, "Worker membership changed during inventory refresh")
	}
	if after.GetOwnerUserId() == "" || after.GetRevision() < 0 {
		return accountInventory{}, cli.Newf(cli.CodeInternal, "the Hub returned an invalid Worker inventory position")
	}
	inventory := accountInventory{owner: after.GetOwnerUserId(), revision: after.GetRevision()}
	for _, worker := range workers {
		if worker.GetId() == "" {
			return accountInventory{}, cli.Newf(cli.CodeInternal, "the Hub returned an empty Worker ID")
		}
		inventory.workerIDs = append(inventory.workerIDs, worker.GetId())
	}
	slices.Sort(inventory.workerIDs)
	return inventory, nil
}

func (r *accountReplay) initialInventory() (accountInventory, error) {
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	for {
		inventory, err := readAccountInventory(r.ctx, r.session)
		if err == nil || r.ctx.Err() != nil {
			return inventory, err
		}
		if r.opts.noReconnect || !cli.FromError(err).Retryable() || !waitAccountReplayRetry(r.ctx, backoff.afterSession(false)) {
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
		if err := r.print(event); err != nil {
			return err
		}
	}
	for key, watch := range r.workers {
		if !current[key.id] {
			r.markResponded(watch)
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
	account.EnrollmentRevision = inventory.revision
	if fresh || changed {
		if err := r.file.save(r.state); err != nil {
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

func (r *accountReplay) watchInventory() {
	timer := time.NewTicker(accountInventoryInterval)
	defer timer.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-timer.C:
		}
		inventory, err := readAccountInventory(r.ctx, r.session)
		if err := r.deliver(r.ctx, func() (bool, error) {
			if err == nil {
				return false, r.applyInventory(inventory, false)
			}
			failure := cli.FromError(err)
			if !failure.Retryable() {
				return false, failure
			}
			event := r.notice(kindStreamUnavailable, failure.Message)
			event.Source, event.Code, event.Message, event.Retrying = "worker", string(failure.Code), failure.Message, boolPtr(true)
			return false, r.print(event)
		}); err != nil {
			return
		}
	}
}

func (r *accountReplay) printed() int {
	if r.hub == nil {
		return r.workerPrinted
	}
	return r.workerPrinted + r.hub.printed
}

func (r *accountReplay) markResponded(watch *accountWorkerWatch) {
	if watch.pending {
		watch.pending = false
		r.pending--
	}
}
