import { afterEach, describe, expect, it, vi } from 'vitest'
import { tryCreateBroadcastChannel } from './broadcastChannel'

/** A constructible stand-in, so the happy path does not need a real channel. */
class FakeBroadcastChannel {
  constructor(readonly name: string) {}
  close(): void {}
}

/**
 * The refusal, hoisted so a test can assert on the IDENTITY of the error the
 * helper forwards, not merely on its message.
 */
const REFUSAL = new Error('BroadcastChannel is not supported in this webview')

/** A class that exists and throws when it is called, as some webviews do. */
class RefusingBroadcastChannel {
  constructor() {
    throw REFUSAL
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

// Both consumers call this at MODULE EVALUATION -- `browserStorageDb` for the
// storage channel, `clientIdentity` for the duplicate-tab handshake -- so an
// uncaught throw here fails the import on exactly the platforms the helper
// exists to tolerate. Each caller loses cross-tab sync and nothing else, which
// is why null is the answer rather than a rejection.
describe('tryCreateBroadcastChannel', () => {
  it('answers a channel where the class constructs', () => {
    vi.stubGlobal('BroadcastChannel', FakeBroadcastChannel)
    const onUnavailable = vi.fn()

    const channel = tryCreateBroadcastChannel('leapmux-probe', onUnavailable)

    expect(channel).toBeInstanceOf(FakeBroadcastChannel)
    // The name reaches the constructor, so two callers cannot land on one bus.
    expect(channel?.name).toBe('leapmux-probe')
    expect(onUnavailable).not.toHaveBeenCalled()
  })

  it('answers null and states the reason where the class does not exist', () => {
    vi.stubGlobal('BroadcastChannel', undefined)
    const onUnavailable = vi.fn()

    expect(tryCreateBroadcastChannel('leapmux-probe', onUnavailable)).toBeNull()
    expect(onUnavailable).toHaveBeenCalledTimes(1)
    expect(onUnavailable.mock.calls[0]?.[0]).toMatch(/no BroadcastChannel/)
    // No error to report: nothing threw, the class was simply absent.
    expect(onUnavailable.mock.calls[0]?.[1]).toBeUndefined()
  })

  // The path a `typeof` test cannot reach, and the reason this helper exists at
  // all. A caller that checked only for the class throws here.
  it('answers null and carries the error where the constructor refuses', () => {
    vi.stubGlobal('BroadcastChannel', RefusingBroadcastChannel)
    const onUnavailable = vi.fn()

    expect(tryCreateBroadcastChannel('leapmux-probe', onUnavailable)).toBeNull()
    expect(onUnavailable).toHaveBeenCalledTimes(1)
    expect(onUnavailable.mock.calls[0]?.[0]).toMatch(/refused to construct/)
    expect(onUnavailable.mock.calls[0]?.[1]).toBe(REFUSAL)
  })

  // `onUnavailable` is optional and `clientIdentity` omits it, so calling an
  // absent callback would turn a tolerated absence back into a throw -- on both
  // paths, because each reports separately.
  it('degrades the same way for a caller that gave no callback', () => {
    vi.stubGlobal('BroadcastChannel', undefined)
    expect(tryCreateBroadcastChannel('leapmux-probe')).toBeNull()

    vi.stubGlobal('BroadcastChannel', RefusingBroadcastChannel)
    expect(tryCreateBroadcastChannel('leapmux-probe')).toBeNull()
  })
})
