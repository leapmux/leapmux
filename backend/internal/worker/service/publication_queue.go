package service

import "sync"

// publicationQueues delivers the items of each entity in FIFO order, with one
// drainer per entity and no lock held across a delivery.
//
// enqueue only appends, and only drain delivers. A caller of drain that finds
// another drainer at work returns at once, and that drainer delivers the caller's
// item. A caller that finds no drainer becomes the drainer: it delivers each item
// of the entity until the queue runs empty, the items that other producers add
// meanwhile included.
//
// So a delivery that parks, as a send does while the connection writer is over
// its byte budget, parks the goroutine that drains and delays that entity alone.
// A producer that finds no drainer can therefore wait for a stalled delivery,
// and that delivery can belong to another producer's item. A caller that must
// not wait, such as a replay, runs the drain on a goroutine of its own.
//
// A queue exists only while it holds undelivered items: drain deletes it when it
// runs empty. An entity that ended therefore leaves nothing here once its last
// item is delivered, and no teardown path has to release it.
type publicationQueues[T any] struct {
	mu     sync.Mutex
	queues map[string]*publicationQueue[T]
}

type publicationQueue[T any] struct {
	items    []T
	draining bool
}

// enqueue appends the item that build returns to the entity's queue.
//
// build runs inside the lock, so what it reads and the item's place in the queue
// are one step: an item that a later change enqueues cannot land ahead of it. It
// must return quickly and must not call back into these queues.
func (q *publicationQueues[T]) enqueue(entityID string, build func() T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	item := build()
	if q.queues == nil {
		q.queues = make(map[string]*publicationQueue[T])
	}
	queue := q.queues[entityID]
	if queue == nil {
		queue = &publicationQueue[T]{}
		q.queues[entityID] = queue
	}
	queue.items = append(queue.items, item)
}

// drain delivers the entity's queued items outside every publication lock.
// A concurrent or reentrant caller returns while the current drainer continues.
func (q *publicationQueues[T]) drain(entityID string, deliver func(T)) {
	q.mu.Lock()
	queue := q.queues[entityID]
	if queue == nil || queue.draining {
		q.mu.Unlock()
		return
	}
	queue.draining = true
	defer func() {
		q.mu.Lock()
		if q.queues[entityID] == queue {
			queue.draining = false
			if len(queue.items) == 0 {
				delete(q.queues, entityID)
			}
		}
		q.mu.Unlock()
	}()
	for {
		if len(queue.items) == 0 {
			delete(q.queues, entityID)
			q.mu.Unlock()
			return
		}
		item := queue.items[0]
		var delivered T
		queue.items[0] = delivered
		queue.items = queue.items[1:]
		q.mu.Unlock()
		deliver(item)
		q.mu.Lock()
	}
}
