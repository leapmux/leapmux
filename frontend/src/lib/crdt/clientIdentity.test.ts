import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_CLIENT_ID, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { createClientIdentity } from './clientIdentity'

/**
 * An in-process BroadcastChannel that delivers BREADTH-FIRST, in FIFO order.
 *
 * Delivery stays synchronous (the real one posts on a task, which would make
 * every assertion here a timing race), but a message posted from inside a
 * handler is QUEUED behind the messages already in flight rather than delivered
 * re-entrantly. That is the one property worth modelling: real channels queue
 * per destination, so a reply cannot overtake a message that was already
 * pending at its recipient.
 *
 * A depth-first fake makes the handshake look correct when it is not. Three
 * tabs sharing an id: duplicate B claims, and depth-first the incumbent A's
 * reply is delivered to B and fully processed BEFORE the second duplicate C has
 * even seen B's claim -- so C's own reply, the one the incumbent would have
 * mistaken for a verdict on itself, is never produced. FIFO reproduces it.
 */
class FakeBroadcastChannel {
  static open: FakeBroadcastChannel[] = []
  private static queue: { to: FakeBroadcastChannel, data: unknown }[] = []
  private static draining = false
  onmessage: ((ev: MessageEvent<unknown>) => void) | null = null
  closed = false
  constructor(readonly name: string) {
    FakeBroadcastChannel.open.push(this)
  }

  /**
   * When false, `postMessage` only enqueues and a test drives delivery with
   * `flush()`. Real delivery is asynchronous, so several tabs can load — and
   * each post its `claim` — before any of them receives anything. Draining
   * inside the constructor makes that unrepresentable: each new tab's handshake
   * fully resolves before the next one exists, so no two duplicates are ever
   * live under the same id at once, which is the only state the three-tab
   * defect needs.
   */
  static autoDrain = true

  static reset(): void {
    FakeBroadcastChannel.open = []
    FakeBroadcastChannel.queue = []
    FakeBroadcastChannel.draining = false
    FakeBroadcastChannel.autoDrain = true
  }

  static flush(): void {
    if (FakeBroadcastChannel.draining)
      return
    FakeBroadcastChannel.draining = true
    try {
      for (let next = FakeBroadcastChannel.queue.shift(); next; next = FakeBroadcastChannel.queue.shift()) {
        if (!next.to.closed)
          next.to.onmessage?.({ data: next.data } as MessageEvent<unknown>)
      }
    }
    finally {
      FakeBroadcastChannel.draining = false
    }
  }

  postMessage(data: unknown): void {
    for (const peer of FakeBroadcastChannel.open) {
      if (peer === this || peer.closed || peer.name !== this.name)
        continue
      FakeBroadcastChannel.queue.push({ to: peer, data })
    }
    // A nested post (one made from inside a handler) only enqueues; the
    // outermost call owns the drain, which is what makes this breadth-first.
    if (FakeBroadcastChannel.autoDrain)
      FakeBroadcastChannel.flush()
  }

  close(): void {
    this.closed = true
    FakeBroadcastChannel.open = FakeBroadcastChannel.open.filter(c => c !== this)
  }
}

beforeEach(() => {
  FakeBroadcastChannel.reset()
  vi.stubGlobal('BroadcastChannel', FakeBroadcastChannel)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('createClientIdentity', () => {
  it('reuses the id already in sessionStorage, so a refresh resumes its own checkpoint', () => {
    sessionStorageSet(KEY_CLIENT_ID, 'c-existing')
    const identity = createClientIdentity()
    expect(identity.clientId()).toBe('c-existing')
    identity.dispose()
  })

  it('mints and persists an id when there is none', () => {
    const identity = createClientIdentity()
    expect(identity.clientId()).toMatch(/^c-/)
    expect(sessionStorageGet<string>(KEY_CLIENT_ID)).toBe(identity.clientId())
    identity.dispose()
  })

  // THE case this handshake exists for. Browsers COPY sessionStorage into a
  // duplicated tab ("Duplicate tab", "Reopen closed tab"), so two LIVE tabs end
  // up holding the same client id -- and therefore the same checkpoint owner
  // key. Each one's writeCheckpointAndTruncateOpLog deletes the whole owner
  // range, so they truncate each other's op-log segments; the next reload
  // replays a checkpoint plus a tail with a hole in it, and the batchEnd frames
  // in that tail advance the resume cursor straight past the missing ops. The
  // hub only ships what is strictly after the cursor, so they are never re-sent.
  it('re-mints in the DUPLICATE when a live tab already holds the id', () => {
    sessionStorageSet(KEY_CLIENT_ID, 'c-shared')
    const incumbent = createClientIdentity()
    expect(incumbent.clientId()).toBe('c-shared')

    // The duplicate loads with the cloned sessionStorage value.
    const duplicate = createClientIdentity()

    expect(incumbent.clientId()).toBe('c-shared')
    expect(duplicate.clientId()).not.toBe('c-shared')
    expect(duplicate.clientId()).toMatch(/^c-/)
    // ...and the duplicate persisted its replacement, so ITS next refresh is
    // stable rather than colliding again.
    expect(sessionStorageGet<string>(KEY_CLIENT_ID)).toBe(duplicate.clientId())

    incumbent.dispose()
    duplicate.dispose()
  })

  // The two-tab case above passes even with a broadcast `held`, because there is
  // only ever one answerer. Add a THIRD tab holding the same id -- duplicating a
  // tab twice in quick succession, or restoring a session that reopens several
  // clones -- and every holder answers every claim. Unaddressed, the incumbent
  // hears the OTHER duplicate's answer and re-mints itself: the checkpoint is
  // keyed (userId, clientId), so the tab that owns the persisted state loses it,
  // misses on hydrate, and cold-starts with the full projection snapshot this
  // whole branch exists to avoid -- while its rows are orphaned until the sweep.
  it('keeps the incumbent\'s id when TWO duplicates race', () => {
    sessionStorageSet(KEY_CLIENT_ID, 'c-shared')
    const incumbent = createClientIdentity()
    expect(incumbent.clientId()).toBe('c-shared')

    // Hold delivery so both duplicates load — and post their claims — before
    // either handshake resolves. That is what puts three tabs on one id at the
    // same instant; with delivery inline, each duplicate re-mints before the
    // next is constructed and they are never concurrently live.
    FakeBroadcastChannel.autoDrain = false
    sessionStorageSet(KEY_CLIENT_ID, 'c-shared')
    const dupA = createClientIdentity()
    sessionStorageSet(KEY_CLIENT_ID, 'c-shared')
    const dupB = createClientIdentity()
    FakeBroadcastChannel.autoDrain = true
    FakeBroadcastChannel.flush()

    expect(incumbent.clientId()).toBe('c-shared')
    expect(dupA.clientId()).not.toBe('c-shared')
    expect(dupB.clientId()).not.toBe('c-shared')
    // And the two duplicates did not collide with each other on the way out.
    expect(dupA.clientId()).not.toBe(dupB.clientId())

    incumbent.dispose()
    dupA.dispose()
    dupB.dispose()
  })

  it('leaves distinct ids alone', () => {
    sessionStorageSet(KEY_CLIENT_ID, 'c-one')
    const a = createClientIdentity()
    sessionStorageSet(KEY_CLIENT_ID, 'c-two')
    const b = createClientIdentity()

    expect(a.clientId()).toBe('c-one')
    expect(b.clientId()).toBe('c-two')
    a.dispose()
    b.dispose()
  })

  // A closed tab must stop defending its id, or the next tab to legitimately
  // inherit it (same sessionStorage, tab reopened) would be told to re-mint by
  // a peer that no longer exists.
  it('stops answering once disposed', () => {
    sessionStorageSet(KEY_CLIENT_ID, 'c-gone')
    const departed = createClientIdentity()
    departed.dispose()

    const successor = createClientIdentity()
    expect(successor.clientId()).toBe('c-gone')
    successor.dispose()
  })

  it('uses the stored id unchanged where BroadcastChannel is unavailable', () => {
    vi.stubGlobal('BroadcastChannel', undefined)
    sessionStorageSet(KEY_CLIENT_ID, 'c-no-channel')
    const identity = createClientIdentity()
    expect(identity.clientId()).toBe('c-no-channel')
    identity.dispose()
  })
})

// The checkpoint sweep's liveness oracle. `writtenAt` moves only on a REWRITE
// -- once per 256 confirmed frames -- so a quiet but LIVE sibling tab looks
// arbitrarily stale, and the sweep used to delete its checkpoint and entire
// op-log out from under it. Asking who is actually running is the fix, and the
// claim/held handshake above already knows.
