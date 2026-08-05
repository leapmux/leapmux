package crdt

import (
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// MarshaledEvent wraps a `*leapmuxv1.WatchUserEvent` with a lazy proto.Marshal
// cache. Multiple subscribers share the same `*MarshaledEvent` for events the
// manager broadcasts to all of them; the first caller pays the marshal cost and
// every other subscriber reuses the cached buffer.
//
// The wrapper is intentionally minimal: callers that only need to inspect the
// proto can read `evt.Event` directly. Callers writing to a wire should call
// `evt.Bytes()`.
//
// # Why it counts its holders
//
// That sharing is exactly what makes the frame's memory un-chargeable to any
// one subscriber: N queues hold ONE buffer, so charging each of them would
// report N times the memory that exists, and the Hub would shed connections on
// a machine with room. The wrapper therefore counts the queues currently
// holding it (Retain/Release) and reports the transitions at which its bytes
// start and stop being resident. It stays free of any budget type: it answers
// how many bytes changed hands, and the queue that asked decides what that
// means for its pool.
type MarshaledEvent struct {
	// Event is the underlying proto. Read-only for consumers; the
	// manager constructs it before any Send call sees the wrapper.
	Event *leapmuxv1.WatchUserEvent

	once  sync.Once
	bytes []byte
	err   error
	// marshaled mirrors `once` having run, readable without paying for it.
	// Exists so a test can assert the invariant the broadcast path now relies
	// on -- that a frame reaching a subscriber has ALREADY been marshaled, so
	// the charge it triggers cannot serialize a proto under the projection
	// lock. An atomic rather than a bytes != nil peek because Bytes is called
	// from writer goroutines too.
	marshaled atomic.Bool

	// holders is how many subscriber queues currently hold this frame. Its
	// 0->1 and 1->0 transitions are the only moments the frame's bytes enter
	// or leave residency, and both are reported to the caller that caused them.
	holders atomic.Int32
}

// NewMarshaledEvent wraps `evt` for delivery. The proto pointer is
// captured by reference; do not mutate `evt` after constructing the
// wrapper.
func NewMarshaledEvent(evt *leapmuxv1.WatchUserEvent) *MarshaledEvent {
	return &MarshaledEvent{Event: evt}
}

// Bytes returns the binary-marshaled representation of the wrapped
// event. The first caller across all subscribers pays the marshal
// cost; subsequent callers receive the cached buffer (and the cached
// error, if marshal failed).
func (e *MarshaledEvent) Bytes() ([]byte, error) {
	e.once.Do(func() {
		e.bytes, e.err = proto.Marshal(e.Event)
		e.marshaled.Store(true)
	})
	return e.bytes, e.err
}

// ResidentFactor scales a frame's marshaled length into what holding it
// actually costs, and is exported so a budget sized against these frames can be
// derived from the same number rather than a second copy of it.
//
// TWO things stay alive for as long as any queue holds the frame: the marshaled
// buffer, and the proto tree it was marshaled from -- Event stays reachable, by
// design, because callers may inspect it. Counting only the buffer had the
// user-events pool bounding roughly half the memory it appeared to, which is
// the one direction a budget must never be wrong in.
//
// Two rather than some finer figure, and it is an estimate rather than a
// measurement -- the same status Config.FrameOverhead has for a sendq.Writer.
// Every wire byte came from somewhere in the tree, so the tree is at least the
// marshaled length; in practice it is more, since a string field costs a 16-byte
// header plus its bytes in memory against a tag and a varint on the wire. Erring
// low would put the budget back to bounding less than it holds, so this rounds
// toward honesty rather than toward generosity.
const ResidentFactor = 2

// Size reports the frame's resident cost in bytes: ResidentFactor times its
// marshaled length. It is what the queue charges and what Retain and Release
// report, so a caller never has to know which of the two numbers it holds.
//
// The marshaled length comes from `len(Bytes())` rather than proto.Size, which
// would look cheaper. Two reasons, and the first is a correctness one:
// proto.Size and proto.Marshal both write each message's sizeCache
// NON-atomically (see the note on materializedFromState), so a size walk on the
// enqueuing goroutine racing a marshal on a WS writer's would be a genuine data
// race. Going through Bytes puts both behind the sync.Once that already orders
// them, leaving exactly one walk of the message per frame rather than adding a
// second.
//
// The second is that it makes the charge and the allocation coincide: the buffer
// being counted is the buffer that now exists, not an estimate of one a writer
// will allocate later. The cost is that the marshal happens when the frame is
// queued rather than when the first writer sends it -- the same single marshal,
// moved onto the broadcasting goroutine, which is why that goroutine pays for it
// BEFORE it takes the projection lock (see broadcastBatch's pre-warm pass).
//
// A frame that fails to marshal sizes as zero, so it is admitted free rather
// than charged for bytes that do not exist. The writer re-reads the cached error
// and fails the connection on it: there is no per-frame skip, and because this
// one buffer fans out to every subscriber of the user, one unmarshalable event
// disconnects all of them. Reachable in principle -- Go's protobuf runtime
// refuses invalid UTF-8 in a proto3 string, which is why the manager screens
// for it before an event ever gets here.
//
// int64 rather than int, like every other byte count the pools traffic in: the
// multiply by ResidentFactor is the widest arithmetic on this path, so returning
// int and letting the caller widen would put the overflow back one layer down --
// on a 32-bit build a 1 GiB frame would wrap before anything saw it.
func (e *MarshaledEvent) Size() int64 {
	data, _ := e.Bytes()
	return int64(len(data)) * ResidentFactor
}

// WireSize reports the marshaled length alone -- what actually goes on the
// socket, as opposed to what holding the frame costs the Hub. Size is the number
// every budget uses; this one exists for callers reasoning about the wire.
func (e *MarshaledEvent) WireSize() int64 {
	data, _ := e.Bytes()
	return int64(len(data))
}

// Retain records one more holder and reports the bytes that BECOME resident:
// Size() when this is the first holder, zero when another queue already holds
// the frame.
//
// Callers pair every Retain with exactly one Release, and must treat a refusal
// as a Release -- a frame a queue declined is a frame it does not hold.
func (e *MarshaledEvent) Retain() (resident int64) {
	if e.holders.Add(1) == 1 {
		return e.Size()
	}
	return 0
}

// Release drops one holder and reports the bytes that STOP being resident:
// Size() when this was the last holder, zero otherwise.
//
// The last holder need not be the one that brought the frame in, so a caller
// must refund what THIS call reports rather than what its own Retain did.
//
// A 1->0->1 sequence is legitimate and charges twice: the manager may still be
// fanning the frame out after every queue so far has drained it, and in that
// window the buffer really is resident again. This refcount governs accounting,
// not lifetime -- nothing is freed at zero -- so there is no use-after-free to
// guard against, only a total to keep honest.
func (e *MarshaledEvent) Release() (freed int64) {
	switch n := e.holders.Add(-1); {
	case n == 0:
		return e.Size()
	case n < 0:
		// Unreachable unless a queue released a frame twice, which would drive
		// its pool's resident total negative and quietly disable the memory
		// bound for every connection drawing on it. Failing loudly beats a
		// budget that silently stops applying.
		panic("crdt: MarshaledEvent released more often than retained")
	}
	return 0
}

// Holders reports how many queues currently hold this frame. For tests and
// assertions; the production paths use Retain/Release's return values, which
// report the transitions rather than a value that can move underneath them.
func (e *MarshaledEvent) Holders() int { return int(e.holders.Load()) }
