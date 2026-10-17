import { createRenderEffect, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createTurnRetryOfferStore } from './chatTurnRetryOffer'

// Frozen, because the store keeps the object that it receives. The store copies
// a frozen object first, so no case can edit the fixture of a later case.
const OFFER = Object.freeze({ turnId: 'turn-1', model: 'gpt-6-luna' })
const LEARN_MORE_URL = 'https://help.openai.com/en/articles/20001326'

describe('createTurnRetryOfferStore', () => {
  it('records an offer and reads it back', () => {
    createRoot((dispose) => {
      const store = createTurnRetryOfferStore()
      store.set('a1', OFFER)
      expect(store.get('a1')).toEqual(OFFER)
      dispose()
    })
  })

  it('answers undefined for an unknown agent', () => {
    createRoot((dispose) => {
      const store = createTurnRetryOfferStore()
      store.set('a1', OFFER)
      expect(store.get('other')).toBeUndefined()
      dispose()
    })
  })

  // The offer identifies one turn. A later offer replaces the earlier one
  // rather than stacking behind it.
  it('keeps one offer per agent', () => {
    createRoot((dispose) => {
      const store = createTurnRetryOfferStore()
      store.set('a1', OFFER)
      store.set('a1', { ...OFFER, turnId: 'turn-2' })
      expect(store.get('a1')).toEqual({ ...OFFER, turnId: 'turn-2' })
      dispose()
    })
  })

  // A later offer replaces the whole value. A field that the later offer
  // omits must not keep the value of the earlier offer, or the banner links
  // the help page of a notice that the worker no longer reports.
  it('drops a field that the replacing offer omits', () => {
    createRoot((dispose) => {
      const store = createTurnRetryOfferStore()
      store.set('a1', { ...OFFER, learnMoreUrl: LEARN_MORE_URL })
      store.set('a1', { ...OFFER, turnId: 'turn-2' })
      expect(store.get('a1')).toEqual({ ...OFFER, turnId: 'turn-2' })
      expect(store.get('a1')?.learnMoreUrl).toBeUndefined()
      dispose()
    })
  })

  // A field that the replacing offer adds must reach the reader also.
  it('adds a field that the replacing offer gives', () => {
    createRoot((dispose) => {
      const store = createTurnRetryOfferStore()
      store.set('a1', OFFER)
      store.set('a1', { ...OFFER, learnMoreUrl: LEARN_MORE_URL })
      expect(store.get('a1')).toEqual({ ...OFFER, learnMoreUrl: LEARN_MORE_URL })
      dispose()
    })
  })

  // A Solid store keeps the object that it receives as its own state, and a
  // later replace edits that object in place. The store must never edit the
  // object of the caller: a caller that keeps the object, as a test fixture
  // does, then sees fields that it never wrote. A frozen object cannot prove
  // this, because the store copies a frozen object before it keeps it.
  it('never edits the offer object of the caller', () => {
    createRoot((dispose) => {
      const store = createTurnRetryOfferStore()
      const first = { turnId: 'turn-1', model: 'gpt-6-luna', learnMoreUrl: LEARN_MORE_URL }
      store.set('a1', first)
      store.set('a1', { turnId: 'turn-2', model: 'gpt-6-nano' })
      expect(first).toEqual({ turnId: 'turn-1', model: 'gpt-6-luna', learnMoreUrl: LEARN_MORE_URL })
      expect(store.get('a1')).toEqual({ turnId: 'turn-2', model: 'gpt-6-nano' })
      dispose()
    })
  })

  // A reader subscribes to the fields that it reads. The replace must notify
  // the reader of a field that the replacing offer removes.
  it('notifies a reader of a field that the replacing offer removes', () => {
    const store = createTurnRetryOfferStore()
    store.set('a1', { ...OFFER, learnMoreUrl: LEARN_MORE_URL })
    const seen: (string | undefined)[] = []
    // The root only owns the reader. A set inside the root body runs in the
    // batch of the root, which defers the rerun of the reader until the body
    // returns, so the replace must happen after the root returns.
    const dispose = createRoot((disposeRoot) => {
      createRenderEffect(() => {
        seen.push(store.get('a1')?.learnMoreUrl)
      })
      return disposeRoot
    })
    store.set('a1', OFFER)
    expect(seen).toEqual([LEARN_MORE_URL, undefined])
    dispose()
  })

  it('withdraws an offer', () => {
    createRoot((dispose) => {
      const store = createTurnRetryOfferStore()
      store.set('a1', OFFER)
      store.clear('a1')
      expect(store.get('a1')).toBeUndefined()
      dispose()
    })
  })

  // Every turn boundary withdraws the offer, and one boundary can run several
  // times per turn. A repeated withdrawal must leave the store able to record
  // the next offer.
  it('leaves the store able to record an offer after a repeated withdrawal', () => {
    createRoot((dispose) => {
      const store = createTurnRetryOfferStore()
      store.set('a1', OFFER)
      store.clear('a1')
      store.clear('a1')
      expect(store.get('a1')).toBeUndefined()
      store.set('a1', { ...OFFER, turnId: 'turn-2' })
      expect(store.get('a1')).toEqual({ ...OFFER, turnId: 'turn-2' })
      dispose()
    })
  })

  // The offer belongs to one agent alone: one agent's retry must never surface
  // in another agent's composer.
  it('keeps two agents apart', () => {
    createRoot((dispose) => {
      const store = createTurnRetryOfferStore()
      store.set('a1', OFFER)
      store.clear('a2')
      expect(store.get('a1')).toEqual(OFFER)
      dispose()
    })
  })
})
