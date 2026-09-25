import { describe, expect, it } from 'vitest'
import { MIMO_EVENT } from '~/generated/contracts/mimo-protocol'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { errorFrame, mimoFrame, statusFrame, TEST_SESSION, toolFrame } from '~/test-support/mimoFixtures'
import { mimoResultDivider } from './resultDivider'

describe('mimoResultDivider', () => {
  it('reads an idle as the turn end', () => {
    expect(mimoResultDivider(statusFrame('idle'))).toEqual({ label: 'Turn ended' })
  })

  // The idle states no outcome of its own. LeapMux knows it stopped the turn.
  it('reads the idle of an interrupted turn from the completion', () => {
    expect(mimoResultDivider(statusFrame('idle'), MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted' })
  })

  it('reads a failure with its name and its reason', () => {
    expect(mimoResultDivider(errorFrame('APIError', 'rate limited'), MessageCompletion.ERROR)).toEqual({
      label: 'Turn failed (APIError) — rate limited',
      isError: true,
    })
  })

  it('reads an abort error as an interruption', () => {
    expect(mimoResultDivider(errorFrame('MessageAbortedError', 'aborted'))).toEqual({ label: 'Turn interrupted' })
  })

  it('reads no other row', () => {
    expect(mimoResultDivider(statusFrame('busy'))).toBeNull()
    expect(mimoResultDivider(toolFrame('bash', {}))).toBeNull()
    expect(mimoResultDivider({ content: 'hi' })).toBeNull()
  })

  it('reads no divider from a status event with no status', () => {
    expect(mimoResultDivider(mimoFrame(MIMO_EVENT.SessionStatus, { sessionID: TEST_SESSION }))).toBeNull()
  })

  // The reader's stop can reach MiMo as a failure of another name. LeapMux's own
  // completion knows that the reader stopped the turn.
  it('reads a failure of an interrupted turn as an interruption', () => {
    expect(mimoResultDivider(errorFrame('APIError', 'socket closed'), MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted' })
  })

  it('reads a failure with no completion as a failure', () => {
    expect(mimoResultDivider(errorFrame('APIError', 'rate limited'))).toEqual({ label: 'Turn failed (APIError) — rate limited', isError: true })
  })

  // An error that states no name or no message draws no empty parentheses and no
  // empty reason.
  it('reads a failure that states only part of its words', () => {
    expect(mimoResultDivider(errorFrame('', 'rate limited'))).toEqual({ label: 'Turn failed — rate limited', isError: true })
    expect(mimoResultDivider(errorFrame('APIError', ''))).toEqual({ label: 'Turn failed (APIError)', isError: true })
    expect(mimoResultDivider(mimoFrame(MIMO_EVENT.SessionError, { sessionID: TEST_SESSION }))).toEqual({ label: 'Turn failed', isError: true })
  })

  it('reads the idle of a completed turn as the turn end', () => {
    expect(mimoResultDivider(statusFrame('idle'), MessageCompletion.COMPLETE)).toEqual({ label: 'Turn ended' })
  })
})
