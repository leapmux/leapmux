import { describe, expect, it } from 'vitest'
import { CLINE_NOTICE_KIND, CLINE_NOTICE_PHASE, CLINE_TEAM_RUN_EVENT } from '~/generated/contracts/cline-protocol'
import { clineCompactionBoundary, clineIsNotice, clineNotificationEntry } from './notification'

function notice(kind: string, phase: string, metadata: Record<string, unknown> = {}, message = kind) {
  return { version: 'v1', event: 'session.notice', sessionId: 's1', payload: { message, noticeType: 'status', metadata: { kind, phase, ...metadata } } }
}

function team(eventType: string, extra: Record<string, unknown> = {}) {
  return { version: 'v1', event: 'team.progress', sessionId: 's1', payload: { summary: { teamName: 'builders' }, lastEvent: { eventType, runId: 'run_1', agentId: 'researcher', ...extra } } }
}

describe('clineNotificationEntry', () => {
  it('reads each phase of a compaction', () => {
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.AutoCompaction, CLINE_NOTICE_PHASE.Started)))
      .toEqual([{ kind: 'compaction', phase: 'start', detail: { trigger: 'auto' } }])
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.ManualCompaction, CLINE_NOTICE_PHASE.Completed, { tokensBefore: 90000, tokensAfter: 12000 })))
      .toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'manual', pre: 90000, post: 12000 } }])
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.OverflowRecoveryCompaction, CLINE_NOTICE_PHASE.Skipped)))
      .toEqual([{ kind: 'status', text: 'Compaction skipped' }])
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.AutoCompaction, CLINE_NOTICE_PHASE.Failed)))
      .toEqual([{ kind: 'status', text: 'Compaction failed' }])
  })

  // A count that the notice does not state is absent, not zero.
  it('reads a finished compaction that states no token count', () => {
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.AutoCompaction, CLINE_NOTICE_PHASE.Completed)))
      .toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto' } }])
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.AutoCompaction, CLINE_NOTICE_PHASE.Completed, { tokensBefore: 0, tokensAfter: '10' })))
      .toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto', pre: 0 } }])
  })

  // A compaction kind with a phase this build does not know reads as a notice of a
  // later Cline: by its message.
  it('reads a compaction of a phase it does not know by its message', () => {
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.AutoCompaction, 'paused', {}, 'Compaction paused.')))
      .toEqual([{ kind: 'status', text: 'Compaction paused.' }])
  })

  it('reads a retry of a failed model call', () => {
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.ProviderErrorRetry, CLINE_NOTICE_PHASE.Started, { attempt: 2, maxRetries: 3, delayMs: 4000, providerError: 'overloaded' })))
      .toEqual([{ kind: 'retry', scope: 'api', attempt: 2, maxAttempts: 3, delayMs: 4000, error: 'overloaded' }])
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.ProviderErrorRetry, CLINE_NOTICE_PHASE.Started)))
      .toEqual([{ kind: 'retry', scope: 'api' }])
  })

  it('reads each recovery as a status', () => {
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.CompactionBudgetEmergency, '')))
      .toEqual([{ kind: 'status', text: 'Compaction trimmed the context further to fit the model' }])
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.ContextOverflowRecovery, CLINE_NOTICE_PHASE.Started)))
      .toEqual([{ kind: 'status', text: 'The context exceeded the model\'s window; compacting and retrying' }])
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.MaxTokensRecovery, CLINE_NOTICE_PHASE.Started)))
      .toEqual([{ kind: 'status', text: 'The answer hit the output limit; retrying' }])
    expect(clineNotificationEntry(notice(CLINE_NOTICE_KIND.MaxTokensRecovery, CLINE_NOTICE_PHASE.Failed)))
      .toEqual([{ kind: 'status', text: 'Recovery from the output limit failed' }])
  })

  it('reads a notice of a later Cline by its message, and one with none as nothing', () => {
    expect(clineNotificationEntry(notice('something_new', '', {}, 'Something new happened.'))).toEqual([{ kind: 'status', text: 'Something new happened.' }])
    expect(clineNotificationEntry(notice('something_new', '', {}, ''))).toEqual([])
  })

  it('reads each run event of a teammate', () => {
    expect(clineNotificationEntry(team(CLINE_TEAM_RUN_EVENT.RunStarted))).toEqual([{ kind: 'text', text: 'researcher started a run' }])
    expect(clineNotificationEntry(team(CLINE_TEAM_RUN_EVENT.RunFailed, { taskId: 'task-1', message: 'boom' }))).toEqual([{ kind: 'text', text: 'researcher failed a run (task-1): boom' }])
    expect(clineNotificationEntry(team(CLINE_TEAM_RUN_EVENT.RunQueued, { agentId: '' }))).toEqual([{ kind: 'text', text: 'A teammate queued a run' }])
    expect(clineNotificationEntry(team('agent_event'))).toEqual([])
  })

  it('reads a team row that states no event as nothing', () => {
    expect(clineNotificationEntry({ version: 'v1', event: 'team.progress', sessionId: 's1', payload: { summary: { teamName: 'builders' } } })).toEqual([])
  })

  it('reads nothing of another row', () => {
    expect(clineNotificationEntry({ version: 'v1', event: 'assistant.finished', payload: { text: 'x' } })).toEqual([])
    expect(clineNotificationEntry({ type: 'settings_changed' })).toEqual([])
  })
})

describe('clineIsNotice', () => {
  it('holds the two notice events alone', () => {
    expect(clineIsNotice(notice(CLINE_NOTICE_KIND.AutoCompaction, CLINE_NOTICE_PHASE.Started))).toBe(true)
    expect(clineIsNotice(team(CLINE_TEAM_RUN_EVENT.RunStarted))).toBe(true)
    expect(clineIsNotice({ version: 'v1', event: 'assistant.finished', payload: {} })).toBe(false)
    expect(clineIsNotice('x')).toBe(false)
  })
})

describe('clineCompactionBoundary', () => {
  const parsed = (parentObject: Record<string, unknown>) => ({ rawText: '', topLevel: parentObject, parentObject, wrapper: null })

  it('reads a finished compaction alone', () => {
    expect(clineCompactionBoundary(parsed(notice(CLINE_NOTICE_KIND.AutoCompaction, CLINE_NOTICE_PHASE.Completed, { tokensBefore: 100, tokensAfter: 10 }))))
      .toEqual({ trigger: 'auto', pre: 100, post: 10 })
    expect(clineCompactionBoundary(parsed(notice(CLINE_NOTICE_KIND.AutoCompaction, CLINE_NOTICE_PHASE.Started)))).toBeNull()
    expect(clineCompactionBoundary(parsed(notice(CLINE_NOTICE_KIND.ProviderErrorRetry, CLINE_NOTICE_PHASE.Completed)))).toBeNull()
  })

  it('reads no boundary from a row that is not a notice', () => {
    expect(clineCompactionBoundary(parsed(team(CLINE_TEAM_RUN_EVENT.RunCompleted)))).toBeNull()
    expect(clineCompactionBoundary(parsed({ type: 'context_cleared' }))).toBeNull()
  })
})
