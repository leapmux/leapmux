// Package readseq orders the output of a feed that starts from a read of the
// current state and then sends the live changes of that state.
//
// A change that arrives during a read can be newer than the read's result,
// because the source can publish it after the read began. If the feed sent
// such a change first, the older result would replace the newer state. A
// Sequencer therefore holds each change that arrives during a read, sends the
// read's result, and then sends the held changes in their order.
package readseq

import (
	"context"
	"sync"
)

// inputBuffer is how many inputs a source can queue before the feed's loop
// takes them, so the goroutine of a stream rarely waits for the loop.
const inputBuffer = 16

// Read reads the current state. It runs on a goroutine of its own, and it
// must return once ctx ends.
type Read[R any] func(ctx context.Context) R

// Hooks is what a Sequencer calls in its feed. The Sequencer calls every hook
// on the goroutine of the feed's loop. Each hook returns false when the feed
// ends.
type Hooks[I, C, R any] struct {
	// Handle takes one input in stream order. It is the function that the
	// feed's loop calls for each input that it takes from Inputs.
	Handle func(ctx context.Context, in I) bool
	// NextRead returns the read that a request asks for, or nil when the
	// request needs no read. A feed can send a result itself in place of a
	// read.
	NextRead func(ctx context.Context) (Read[R], bool)
	// Apply sends the result of a read.
	Apply func(ctx context.Context, result R) bool
	// Send delivers one change to the consumer.
	Send func(ctx context.Context, change C) bool
}

// Result is the result of one read, as Results delivers it.
type Result[R any] struct {
	// epoch identifies the read. Abandon makes every earlier read stale.
	epoch int
	value R
}

// Sequencer holds the changes of a feed behind each read of the current
// state. I is the type of the feed's input, C the type of a change, and R
// the type of a read's result.
//
// The feed's loop takes each input from Inputs and calls Hooks.Handle, and
// takes each result from Results and calls Complete. It defers Wait. Only
// the goroutine of the loop calls the methods of a Sequencer, except Push,
// which any goroutine may call. So no lock guards the state.
type Sequencer[I, C, R any] struct {
	hooks   Hooks[I, C, R]
	inputs  chan I
	results chan Result[R]
	// waitCtx ends when Wait begins, canceling every in-flight read and
	// unblocking any result delivery even if the caller's context stays live.
	waitCtx context.Context
	waitEnd context.CancelFunc
	// reads counts the goroutines of the reads.
	reads sync.WaitGroup
	// epoch identifies the reads whose results still apply.
	epoch int
	// reading reports a read in flight, and readAgain a request that arrived
	// during it.
	reading, readAgain bool
	// held holds the changes that arrived during the read in flight.
	held []C
}

// New returns a Sequencer that calls hooks. It panics when a hook is nil,
// because the Sequencer cannot work without each of them.
func New[I, C, R any](hooks Hooks[I, C, R]) *Sequencer[I, C, R] {
	if hooks.Handle == nil || hooks.NextRead == nil || hooks.Apply == nil || hooks.Send == nil {
		panic("readseq: New needs every hook")
	}
	waitCtx, waitEnd := context.WithCancel(context.Background())
	return &Sequencer[I, C, R]{
		hooks:   hooks,
		inputs:  make(chan I, inputBuffer),
		results: make(chan Result[R], 1),
		waitCtx: waitCtx,
		waitEnd: waitEnd,
	}
}

// Push hands one input to the feed's loop, in the order of the calls. It
// returns false when ctx ended first. Any goroutine may call it.
func (s *Sequencer[I, C, R]) Push(ctx context.Context, in I) bool {
	select {
	case s.inputs <- in:
		return true
	case <-ctx.Done():
		return false
	}
}

// Inputs delivers each input that Push received, in order.
func (s *Sequencer[I, C, R]) Inputs() <-chan I { return s.inputs }

// Results delivers the result of each read. The loop hands each one to
// Complete.
func (s *Sequencer[I, C, R]) Results() <-chan Result[R] { return s.results }

// Forward sends change now, or holds it behind the read in flight.
func (s *Sequencer[I, C, R]) Forward(ctx context.Context, change C) bool {
	if s.reading {
		s.held = append(s.held, change)
		return true
	}
	return s.hooks.Send(ctx, change)
}

// RequestRead asks for a read. With no read in flight, it starts the read
// that Hooks.NextRead returns. During a read, it asks Hooks.NextRead again
// after the read's result, once for however many requests arrive meanwhile:
// a read that starts after all of them covers each one.
func (s *Sequencer[I, C, R]) RequestRead(ctx context.Context) bool {
	if s.reading {
		s.readAgain = true
		return true
	}
	return s.begin(ctx)
}

// begin starts the read that Hooks.NextRead returns, when it returns one.
func (s *Sequencer[I, C, R]) begin(ctx context.Context) bool {
	read, ok := s.hooks.NextRead(ctx)
	if !ok {
		return false
	}
	if read == nil {
		return true
	}
	s.reading = true
	epoch := s.epoch
	readCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.waitCtx, cancel)
	s.reads.Go(func() {
		defer stop()
		defer cancel()
		value := read(readCtx)
		select {
		case s.results <- Result[R]{epoch: epoch, value: value}:
		case <-readCtx.Done():
		}
	})
	return true
}

// Complete sends the result of a read, then the changes that it held, and
// starts the read that a request during it asks for. It ignores the result
// of a stale read.
//
// The input that is already queued arrived before the loop acts on result,
// so Complete takes it first. A request among it then asks for the read
// that follows this one, and an Abandon makes this one stale. A select over
// both channels picks at random, so without this an input queued beside the
// result could start one read more than the requests need, or apply a
// result that an Abandon dropped.
func (s *Sequencer[I, C, R]) Complete(ctx context.Context, result Result[R]) bool {
	if result.epoch != s.epoch {
		return true
	}
	// Drain only the inputs already queued at entry so continuous Push traffic
	// cannot starve Apply or grow held changes without limit.
	for n := len(s.inputs); n > 0; n-- {
		select {
		case in := <-s.inputs:
			if !s.hooks.Handle(ctx, in) {
				return false
			}
		default:
			n = 0
		}
	}
	if result.epoch != s.epoch {
		return true
	}
	if !s.hooks.Apply(ctx, result.value) {
		return false
	}
	s.reading = false
	if !s.flush(ctx) {
		return false
	}
	if s.readAgain {
		s.readAgain = false
		return s.begin(ctx)
	}
	return true
}

// Abandon drops the read in flight: Complete ignores its result, and a
// request that arrived during it starts no read. The changes that the read
// held are real, so Abandon sends them now.
func (s *Sequencer[I, C, R]) Abandon(ctx context.Context) bool {
	s.epoch++
	s.reading, s.readAgain = false, false
	return s.flush(ctx)
}

// flush sends every held change, in order.
func (s *Sequencer[I, C, R]) flush(ctx context.Context) bool {
	held := s.held
	s.held = nil
	for _, change := range held {
		if !s.hooks.Send(ctx, change) {
			return false
		}
	}
	return true
}

// Wait cancels any in-flight reads and waits until every read goroutine
// returns. The loop defers Wait, so the loop ends after every read that it
// started without deadlocking when an unread result fills the results buffer.
func (s *Sequencer[I, C, R]) Wait() {
	s.waitEnd()
	s.reads.Wait()
}
