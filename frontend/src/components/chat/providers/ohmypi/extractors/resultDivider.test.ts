import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { ohMyPiResultDivider } from './resultDivider'

function end(stopReason: string, extra: Record<string, unknown> = {}) {
  return {
    type: 'agent_end',
    isTerminal: true,
    messages: [{ role: 'user', content: [] }, { role: 'assistant', content: [], stopReason, ...extra }],
  }
}

describe('ohMyPiResultDivider', () => {
  it('reads a turn that ended', () => {
    expect(ohMyPiResultDivider({ ...end('stop'), duration_ms: 2000 })).toEqual({ label: 'Turn ended (2.0s)' })
  })

  it('reads a turn that failed, with omp\'s reason', () => {
    expect(ohMyPiResultDivider(end('error', { errorMessage: '400 bad request' }))).toEqual({ label: 'Turn failed — 400 bad request', isError: true })
  })

  it('reads an aborted turn and a length limit', () => {
    expect(ohMyPiResultDivider(end('aborted'))).toEqual({ label: 'Turn interrupted' })
    expect(ohMyPiResultDivider(end('length'))).toEqual({ label: 'Turn ended (length limit)' })
  })

  it('states that more work follows an end that omp continues, whatever the cause', () => {
    // A queued steer or follow-up, an agent message, a retry and a compaction each
    // continue the run, and the frame states none of them.
    expect(ohMyPiResultDivider({ ...end('stop'), isTerminal: false, duration_ms: 4200 })).toEqual({ label: 'Turn ended (4.2s, more work follows)' })
    expect(ohMyPiResultDivider({ ...end('error', { errorMessage: '429 rate limited' }), isTerminal: false })).toEqual({
      label: 'Turn failed (more work follows) — 429 rate limited',
      isError: true,
    })
  })

  it('states nothing more for an end with `isTerminal: true`, or with no `isTerminal`', () => {
    expect(ohMyPiResultDivider(end('stop'))).toEqual({ label: 'Turn ended' })
    const unmarked: Record<string, unknown> = end('stop')
    delete unmarked.isTerminal
    expect(ohMyPiResultDivider(unmarked)).toEqual({ label: 'Turn ended' })
  })

  it('reads a stop the reader asked for, whatever the frame says', () => {
    expect(ohMyPiResultDivider(end('error', { errorMessage: 'aborted' }), MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted' })
    expect(ohMyPiResultDivider({ ...end('stop'), duration_ms: 2000 }, MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted (2.0s)' })
  })

  it('reads the frame for a completion other than a stop the reader asked for', () => {
    for (const completion of [MessageCompletion.UNSPECIFIED, MessageCompletion.COMPLETE, MessageCompletion.ERROR])
      expect(ohMyPiResultDivider(end('stop'), completion), MessageCompletion[completion]).toEqual({ label: 'Turn ended' })
  })

  it('reads how a run ended from its LAST assistant message', () => {
    // A run that retried holds the failed reply first and the reply that answered last.
    const frame = {
      type: 'agent_end',
      isTerminal: true,
      messages: [
        { role: 'assistant', stopReason: 'error', errorMessage: '429 rate limited' },
        { role: 'toolResult', content: [] },
        'not a message',
        { role: 'assistant', stopReason: 'stop' },
        { role: 'user', content: [] },
      ],
    }
    expect(ohMyPiResultDivider(frame)).toEqual({ label: 'Turn ended' })
  })

  it('reads a failed turn with no reason, an interrupted turn and a length limit that more work follows', () => {
    expect(ohMyPiResultDivider(end('error'))).toEqual({ label: 'Turn failed', isError: true })
    expect(ohMyPiResultDivider({ ...end('aborted'), isTerminal: false })).toEqual({ label: 'Turn interrupted (more work follows)' })
    expect(ohMyPiResultDivider({ ...end('length'), isTerminal: false })).toEqual({ label: 'Turn ended (length limit, more work follows)' })
  })

  it('reads a run with no assistant message', () => {
    expect(ohMyPiResultDivider({ type: 'agent_end', messages: [] })).toEqual({ label: 'Turn ended' })
    expect(ohMyPiResultDivider({ type: 'agent_end' })).toEqual({ label: 'Turn ended' })
  })

  it('answers null for another frame', () => {
    expect(ohMyPiResultDivider({ type: 'agent_start' })).toBeNull()
    expect(ohMyPiResultDivider('agent_end')).toBeNull()
  })
})
