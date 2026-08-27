import { Code, ConnectError } from '@connectrpc/connect'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { elevationPrompting, isElevationRequired, promptForElevation, setElevationPrompter } from './elevationPrompt'

function withMarker(): ConnectError {
  const meta = new Headers()
  meta.set('leapmux-elevation-required', '1')
  return new ConnectError('needs a recent sign-in', Code.FailedPrecondition, meta)
}

describe('isElevationRequired', () => {
  it('matches only a FailedPrecondition carrying the marker', () => {
    expect(isElevationRequired(withMarker())).toBe(true)
  })

  it('refuses a FailedPrecondition a prompt cannot fix', () => {
    // The refusals a step-up would NOT resolve share the code: "set a
    // replacement password first", "account credentials changed". Prompting
    // for a factor there asks for something that changes nothing.
    expect(isElevationRequired(new ConnectError('cannot delete your only passkey', Code.FailedPrecondition))).toBe(false)
  })

  it('refuses every other shape', () => {
    const meta = new Headers()
    meta.set('leapmux-elevation-required', '1')
    expect(isElevationRequired(new ConnectError('wrong password', Code.Unauthenticated, meta))).toBe(false)
    expect(isElevationRequired(new Error('not a connect error'))).toBe(false)
    expect(isElevationRequired(undefined)).toBe(false)
  })
})

/**
 * The "nobody can prompt" fall-through.
 *
 * With no host registered, `promptForElevation` must answer `false` rather
 * than await a promise nothing can settle. The transport rethrows the hub's
 * own refusal on that answer, which is how a page with no host -- the desktop
 * launcher, or an app root whose ErrorBoundary already caught -- behaves the
 * way every surface did before the prompt existed.
 */
describe('promptForElevation with nothing registered', () => {
  afterEach(() => setElevationPrompter(null))

  it('answers false instead of hanging', async () => {
    await expect(promptForElevation()).resolves.toBe(false)
    // Nothing opened, so nothing may leave the shared busy flag set: a
    // surface disables its sensitive controls on it.
    expect(elevationPrompting()).toBe(false)
  })

  it('answers false again after the host unregisters', async () => {
    setElevationPrompter(async () => true)
    setElevationPrompter(null)
    await expect(promptForElevation()).resolves.toBe(false)
  })

  // A dismissal answers false while the prompter is STILL registered, so the
  // next refusal must open a prompt rather than fall through.
  it('opens the next prompt after a dismissal', async () => {
    const prompt = vi.fn(async () => false)
    setElevationPrompter(prompt)
    await expect(promptForElevation()).resolves.toBe(false)
    await expect(promptForElevation()).resolves.toBe(false)
    expect(prompt).toHaveBeenCalledTimes(2)
  })
})

/**
 * A prompter that fails must read as a dismissal.
 *
 * `ElevationPrompter` returns `Promise<boolean>`, and the type permits a
 * prompter to break that contract in two ways. Neither may reach the caller.
 * The transport awaits `promptForElevation` inside the `catch` that holds the
 * hub's FailedPrecondition, so an escaping error REPLACES the reason the
 * request failed with the prompter's internal one.
 */
describe('promptForElevation on a broken prompter', () => {
  afterEach(() => {
    setElevationPrompter(null)
    vi.restoreAllMocks()
  })

  it('answers false when the prompter rejects', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    setElevationPrompter(async () => {
      throw new Error('the dialog crashed')
    })

    await expect(promptForElevation()).resolves.toBe(false)
    expect(elevationPrompting()).toBe(false)
  })

  // Worse than a rejection: `setPrompting(true)` already ran and the assignment
  // to the shared in-flight promise never happens, so nothing clears either
  // signal and every sensitive control stays disabled for the life of the page.
  it('answers false when the prompter throws before it builds a promise', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    setElevationPrompter((() => {
      throw new Error('the dialog crashed')
    }) as never)

    await expect(promptForElevation()).resolves.toBe(false)
    expect(elevationPrompting()).toBe(false)
  })

  // The shared in-flight promise must be cleared too, so the NEXT refusal opens
  // a prompt instead of getting the failed one back.
  it('opens a fresh prompt after a failure', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    setElevationPrompter(async () => {
      throw new Error('the dialog crashed')
    })
    await promptForElevation()

    setElevationPrompter(async () => true)
    await expect(promptForElevation()).resolves.toBe(true)
  })
})
