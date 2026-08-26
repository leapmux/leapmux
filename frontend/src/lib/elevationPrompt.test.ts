import { Code, ConnectError } from '@connectrpc/connect'
import { afterEach, describe, expect, it } from 'vitest'
import { canPromptForElevation, isElevationRequired, promptForElevation, setElevationPrompter } from './elevationPrompt'

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
 * `promptForElevation` answers `false` for two different states — nobody
 * dismissed it, and nobody could open it — so a caller that prompts BEFORE
 * it acts needs the second one told apart. A dismissal means "do not go on";
 * an absent prompter means "go on and let the hub refuse".
 */
describe('canPromptForElevation', () => {
  afterEach(() => setElevationPrompter(null))

  it('is false with nothing registered', () => {
    expect(canPromptForElevation()).toBe(false)
  })

  it('is true while a prompter is registered', () => {
    setElevationPrompter(async () => true)
    expect(canPromptForElevation()).toBe(true)
  })

  it('goes back to false when the host unregisters', () => {
    setElevationPrompter(async () => true)
    setElevationPrompter(null)
    expect(canPromptForElevation()).toBe(false)
  })

  // The distinction this exists for: a dismissal answers false while the
  // prompter is still there, so the two states are not the same `false`.
  it('stays true across a dismissed prompt', async () => {
    setElevationPrompter(async () => false)
    await expect(promptForElevation()).resolves.toBe(false)
    expect(canPromptForElevation()).toBe(true)
  })
})
