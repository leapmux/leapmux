import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { clineEndsRun, clineResultDivider } from './resultDivider'

const end = (name: string, payload: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({ version: 'v1', event: name, sessionId: 's1', payload, ...extra })

describe('clineResultDivider', () => {
  it('reads a run that completed, with the duration the worker measured', () => {
    expect(clineResultDivider(end('run.completed', { reason: 'completed' }, { duration_ms: 2000 }))).toEqual({ label: 'Turn ended (2.0s)' })
    expect(clineResultDivider(end('run.completed', { reason: 'completed' }))).toEqual({ label: 'Turn ended' })
  })

  it('states a limit that ended the run', () => {
    expect(clineResultDivider(end('run.completed', { reason: 'max_iterations' }))).toEqual({ label: 'Turn ended (iteration limit)' })
  })

  it('reads a run that failed, with Cline\'s error', () => {
    expect(clineResultDivider(end('run.failed', { reason: 'error', error: 'mock failure' })))
      .toEqual({ label: 'Turn failed — mock failure', isError: true })
    expect(clineResultDivider(end('run.failed', { reason: 'mistake_limit', result: { text: 'Too many mistakes.' } })))
      .toEqual({ label: 'Turn failed (mistake limit) — Too many mistakes.', isError: true })
    expect(clineResultDivider(end('run.completed', { reason: 'completed' }), MessageCompletion.ERROR)).toEqual({ label: 'Turn failed', isError: true })
  })

  it('reads a run that was aborted, or that the reader stopped, as interrupted', () => {
    expect(clineResultDivider(end('run.aborted', { reason: 'aborted' }))).toEqual({ label: 'Turn interrupted' })
    expect(clineResultDivider(end('run.failed', { reason: 'error', error: 'aborted' }), MessageCompletion.INTERRUPTED)).toEqual({ label: 'Turn interrupted' })
  })

  // Each of the three signals is enough alone: the event, the reason, or the completion.
  it('reads an abort reason on another end event as interrupted', () => {
    expect(clineResultDivider(end('run.completed', { reason: 'aborted' }, { duration_ms: 1500 }))).toEqual({ label: 'Turn interrupted (1.5s)' })
    expect(clineResultDivider(end('run.aborted', {}))).toEqual({ label: 'Turn interrupted' })
  })

  it('reads a failed run that states no words as a plain failure', () => {
    expect(clineResultDivider(end('run.failed', { reason: 'error' }))).toEqual({ label: 'Turn failed', isError: true })
    expect(clineResultDivider(end('run.failed', {}))).toEqual({ label: 'Turn failed', isError: true })
  })

  it('answers null for another row', () => {
    expect(clineResultDivider(end('assistant.finished', { text: 'x' }))).toBeNull()
    expect(clineResultDivider(null)).toBeNull()
    expect(clineResultDivider('run.completed')).toBeNull()
  })
})

describe('clineEndsRun', () => {
  it('holds the three end events of a run alone', () => {
    expect(['run.completed', 'run.failed', 'run.aborted'].every(clineEndsRun)).toBe(true)
    expect(clineEndsRun('run.started')).toBe(false)
  })
})
