import { Code, ConnectError } from '@connectrpc/connect'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setElevationPrompter } from '~/lib/elevationPrompt'
import * as transportModule from './transport'

/** An error carrying the hub's credential-rejected marker. */
function credentialRejected(): ConnectError {
  const meta = new Headers()
  meta.set('leapmux-credential-rejected', '1')
  return new ConnectError('current password is incorrect', Code.Unauthenticated, meta)
}

describe('isSessionEnded', () => {
  it('signs the user out for an expired or invalid session', () => {
    expect(transportModule.isSessionEnded(new ConnectError('invalid or expired token', Code.Unauthenticated))).toBe(true)
  })

  // The bug this guard exists for: mistyping the password in a
  // verification prompt would end the very session the prompt protects.
  it('does not sign the user out for a credential the request carried', () => {
    expect(transportModule.isSessionEnded(credentialRejected())).toBe(false)
  })

  it('ignores every other failure', () => {
    expect(transportModule.isSessionEnded(new ConnectError('needs a recent sign-in', Code.FailedPrecondition))).toBe(false)
    expect(transportModule.isSessionEnded(new ConnectError('nope', Code.PermissionDenied))).toBe(false)
    expect(transportModule.isSessionEnded(new Error('network down'))).toBe(false)
    expect(transportModule.isSessionEnded(undefined)).toBe(false)
  })
})

describe('transport', () => {
  it('does not export getToken', () => {
    expect('getToken' in transportModule).toBe(false)
  })

  it('does not export setToken', () => {
    expect('setToken' in transportModule).toBe(false)
  })

  it('does not export clearToken', () => {
    expect('clearToken' in transportModule).toBe(false)
  })

  it('exports transport', () => {
    expect(transportModule.transport).toBeDefined()
  })

  it('exports setOnAuthError', () => {
    expect(typeof transportModule.setOnAuthError).toBe('function')
  })

  it('does not export authInterceptor', () => {
    expect('authInterceptor' in transportModule).toBe(false)
  })

  it('does not export TOKEN_KEY', () => {
    expect('TOKEN_KEY' in transportModule).toBe(false)
  })
})

/**
 * The elevation retry.
 *
 * It moved here from a per-call-site wrapper, and that is the point: every
 * sensitive call used to opt in, so one that forgot rendered the hub's raw
 * refusal beside a form with no way forward — and nothing in the frontend
 * listed the procedures that required elevation, so no guard could catch it.
 * The hub's own marker is the classification now, and no call site can miss it.
 */
describe('elevationInterceptor', () => {
  /** A refusal carrying the hub's elevation-required marker. */
  function elevationRequired(): ConnectError {
    const meta = new Headers()
    meta.set('leapmux-elevation-required', '1')
    return new ConnectError('this action needs a recent sign-in', Code.FailedPrecondition, meta)
  }

  /**
   * A request in the shape Connect hands an interceptor: a deadline signal
   * minted before the chain ran, and the per-call budget in the header.
   *
   * Both matter to the retry, so this helper fakes neither away. `timeoutMs` is what
   * `createConnectTransport({ defaultTimeoutMs })` writes.
   */
  function makeUnaryReq(opts: { signal?: AbortSignal, timeoutMs?: number } = {}) {
    const header = new Headers()
    if (opts.timeoutMs !== undefined)
      header.set('connect-timeout-ms', String(opts.timeoutMs))
    return {
      stream: false,
      signal: opts.signal ?? new AbortController().signal,
      header,
    } as never
  }

  const unaryReq = makeUnaryReq()
  const streamReq = { stream: true } as never

  afterEach(() => setElevationPrompter(null))

  it('retries the refused request once a factor is proven', async () => {
    setElevationPrompter(async () => true)
    let calls = 0
    const next = vi.fn(async () => {
      calls++
      if (calls === 1)
        throw elevationRequired()
      return 'ok' as never
    })

    await expect(transportModule.elevationInterceptor(next)(unaryReq)).resolves.toBe('ok')
    expect(calls).toBe(2)
  })

  // EXACTLY ONE retry. A second refusal is the user's to read: proving a
  // factor did not resolve it, so trying again cannot either.
  it('reports a second refusal instead of looping', async () => {
    setElevationPrompter(async () => true)
    const next = vi.fn(async () => {
      throw elevationRequired()
    })

    await expect(transportModule.elevationInterceptor(next)(unaryReq)).rejects.toThrow('recent sign-in')
    expect(next).toHaveBeenCalledTimes(2)
  })

  it('rethrows when the prompt is dismissed', async () => {
    setElevationPrompter(async () => false)
    const next = vi.fn(async () => {
      throw elevationRequired()
    })

    await expect(transportModule.elevationInterceptor(next)(unaryReq)).rejects.toThrow('recent sign-in')
    expect(next).toHaveBeenCalledTimes(1)
  })

  // A surface outside the prompt host must still see WHY its call failed,
  // rather than hanging on a prompt nothing can open.
  it('rethrows when nothing can prompt', async () => {
    const next = vi.fn(async () => {
      throw elevationRequired()
    })

    await expect(transportModule.elevationInterceptor(next)(unaryReq)).rejects.toThrow('recent sign-in')
    expect(next).toHaveBeenCalledTimes(1)
  })

  // A stream's messages are an async iterable that cannot be consumed twice,
  // so a replay would send an empty body.
  it('never retries a stream', async () => {
    setElevationPrompter(async () => true)
    const next = vi.fn(async () => {
      throw elevationRequired()
    })

    await expect(transportModule.elevationInterceptor(next)(streamReq)).rejects.toThrow('recent sign-in')
    expect(next).toHaveBeenCalledTimes(1)
  })

  it('passes every other failure straight through', async () => {
    const prompted = vi.fn(async () => true)
    setElevationPrompter(prompted)
    const next = vi.fn(async () => {
      throw new ConnectError('this account has no password', Code.FailedPrecondition)
    })

    await expect(transportModule.elevationInterceptor(next)(unaryReq)).rejects.toThrow('no password')
    expect(prompted).not.toHaveBeenCalled()
    expect(next).toHaveBeenCalledTimes(1)
  })

  /**
   * The retry's deadline.
   *
   * Connect mints ONE deadline signal per call, before any interceptor runs,
   * and the prompt sits inside that call — so a user who reads the dialog and
   * types a password for longer than the budget used to find the request
   * already aborted. Adding a passkey then failed with DeadlineExceeded, and
   * nothing actually waited for anything.
   */
  describe('the retry deadline', () => {
    it('does not charge the user thinking time to the request', async () => {
      // The call's own deadline expires DURING the prompt, exactly as it does
      // when somebody fetches their password from a manager.
      const original = new AbortController()
      setElevationPrompter(async () => {
        original.abort(new ConnectError('the operation timed out', Code.DeadlineExceeded))
        return true
      })

      let seen: AbortSignal | undefined
      let calls = 0
      const next = vi.fn(async (req: { signal: AbortSignal }) => {
        calls++
        if (calls === 1)
          throw elevationRequired()
        seen = req.signal
        return 'ok' as never
      })

      const req = makeUnaryReq({ signal: original.signal, timeoutMs: 30_000 })
      await expect(transportModule.elevationInterceptor(next as never)(req)).resolves.toBe('ok')
      expect(seen?.aborted).toBe(false)
    })

    // A caller that cancelled deliberately must still cancel the retry: the
    // fresh deadline replaces the expiry, not the caller's own control.
    it('still carries a caller cancellation into the retry', async () => {
      const original = new AbortController()
      setElevationPrompter(async () => {
        original.abort(new ConnectError('canceled', Code.Canceled))
        return true
      })

      let seen: AbortSignal | undefined
      let calls = 0
      const next = vi.fn(async (req: { signal: AbortSignal }) => {
        calls++
        if (calls === 1)
          throw elevationRequired()
        seen = req.signal
        return 'ok' as never
      })

      const req = makeUnaryReq({ signal: original.signal, timeoutMs: 30_000 })
      await transportModule.elevationInterceptor(next as never)(req)
      expect(seen?.aborted).toBe(true)
      expect((seen?.reason as ConnectError).code).toBe(Code.Canceled)
    })

    // The retry keeps the budget the CALL declared, so a caller that asked
    // for a longer or shorter deadline keeps it.
    it('gives the retry the deadline the call declared', async () => {
      vi.useFakeTimers()
      try {
        setElevationPrompter(async () => true)
        let seen: AbortSignal | undefined
        let calls = 0
        const next = vi.fn(async (req: { signal: AbortSignal }) => {
          calls++
          if (calls === 1)
            throw elevationRequired()
          seen = req.signal
          // Resolve without awaiting the timer; the signal outlives the call
          // only for the length of this assertion.
          return 'ok' as never
        })

        const req = makeUnaryReq({ timeoutMs: 5_000 })
        const done = transportModule.elevationInterceptor(next as never)(req)
        await vi.advanceTimersByTimeAsync(0)
        await done
        expect(seen?.aborted).toBe(false)
      }
      finally {
        vi.useRealTimers()
      }
    })

    // No declared deadline means no deadline on the retry either — the
    // unload transport sets none, and inventing one here would abort a call
    // the caller deliberately left open.
    it('adds no deadline when the call declared none', async () => {
      setElevationPrompter(async () => true)
      let seen: AbortSignal | undefined
      let calls = 0
      const next = vi.fn(async (req: { signal: AbortSignal }) => {
        calls++
        if (calls === 1)
          throw elevationRequired()
        seen = req.signal
        return 'ok' as never
      })

      await transportModule.elevationInterceptor(next as never)(makeUnaryReq())
      expect(seen?.aborted).toBe(false)
    })

    // A blank header is "declared nothing", and `Number('')` is 0 — which as
    // a budget means "already expired". Reading it that way would abort the
    // retry before it left.
    it('treats a blank deadline header as no deadline', async () => {
      setElevationPrompter(async () => true)
      let seen: AbortSignal | undefined
      let calls = 0
      const next = vi.fn(async (req: { signal: AbortSignal }) => {
        calls++
        if (calls === 1)
          throw elevationRequired()
        seen = req.signal
        return 'ok' as never
      })

      const header = new Headers()
      header.set('connect-timeout-ms', '  ')
      const req = { stream: false, signal: new AbortController().signal, header } as never
      await transportModule.elevationInterceptor(next as never)(req)
      expect(seen?.aborted).toBe(false)
    })
  })
})

/**
 * The deadline the hub reports on a SLIDE.
 *
 * A step-up ceremony reports the window it granted in its response body. Every
 * sensitive action after that slides the window forward, and the hub emits no
 * event for a slide, because a client still holding the shorter deadline fails
 * closed. So the displayed deadline used to stay where it was, up to two hours
 * early, for the whole window. The hub reports the slid deadline on the
 * response to the request that caused it, and this interceptor adopts it.
 */
describe('elevationDeadlineInterceptor', () => {
  const req = { stream: false } as never

  /** A response carrying the hub's report. */
  function reporting(value: string) {
    const header = new Headers()
    header.set('leapmux-elevation-expires-at', value)
    return { header } as never
  }

  /** The adoption seam AuthContext registers, recorded rather than applied. */
  function recordAdoptions(): { opened: number, adopted: Date[] } {
    const seen = { opened: 0, adopted: [] as Date[] }
    transportModule.setElevationAdoptionOpener(() => {
      seen.opened++
      return (until: Date) => {
        seen.adopted.push(until)
      }
    })
    return seen
  }

  afterEach(() => transportModule.setElevationAdoptionOpener(null))

  it('adopts the deadline the hub reports on a success', async () => {
    const seen = recordAdoptions()
    const next = vi.fn(async () => reporting('2026-08-27T15:55:00Z'))

    await transportModule.elevationDeadlineInterceptor(next)(req)

    expect(seen.adopted).toHaveLength(1)
    expect(seen.adopted[0]!.toISOString()).toBe('2026-08-27T15:55:00.000Z')
  })

  it('adopts nothing when the response carries no report', async () => {
    const seen = recordAdoptions()
    const next = vi.fn(async () => ({ header: new Headers() }) as never)

    await transportModule.elevationDeadlineInterceptor(next)(req)

    expect(seen.adopted).toHaveLength(0)
  })

  // `new Date('...')` answers Invalid Date rather than throwing, and adopting
  // one renders "Invalid Date" on the Preferences row.
  it('adopts nothing from an unreadable value', async () => {
    const seen = recordAdoptions()
    const next = vi.fn(async () => reporting('whenever'))

    await transportModule.elevationDeadlineInterceptor(next)(req)

    expect(seen.adopted).toHaveLength(0)
  })

  // The hub slides AFTER its write commits, so a handler that fails afterwards
  // still left a longer window behind.
  it('adopts the report a failure carries, and still rethrows', async () => {
    const seen = recordAdoptions()
    const meta = new Headers()
    meta.set('leapmux-elevation-expires-at', '2026-08-27T15:55:00Z')
    const next = vi.fn(async () => {
      throw new ConnectError('the hub could not send the email', Code.Unavailable, meta)
    })

    await expect(transportModule.elevationDeadlineInterceptor(next)(req)).rejects.toThrow('could not send')
    expect(seen.adopted).toHaveLength(1)
  })

  it('passes a plain failure through untouched', async () => {
    const seen = recordAdoptions()
    const next = vi.fn(async () => {
      throw new Error('network down')
    })

    await expect(transportModule.elevationDeadlineInterceptor(next)(req)).rejects.toThrow('network down')
    expect(seen.adopted).toHaveLength(0)
  })

  // The opener runs when the request LEAVES, which is what lets AuthContext
  // discard a report that an "End now" beat while the request was in flight.
  it('opens the adoption before the request leaves', async () => {
    const seen = recordAdoptions()
    let openedWhenSent = -1
    const next = vi.fn(async () => {
      openedWhenSent = seen.opened
      return reporting('2026-08-27T15:55:00Z')
    })

    await transportModule.elevationDeadlineInterceptor(next)(req)

    expect(openedWhenSent).toBe(1)
  })

  it('runs without an adopter registered', async () => {
    transportModule.setElevationAdoptionOpener(null)
    const next = vi.fn(async () => reporting('2026-08-27T15:55:00Z'))

    await expect(transportModule.elevationDeadlineInterceptor(next)(req)).resolves.toBeDefined()
  })
})
