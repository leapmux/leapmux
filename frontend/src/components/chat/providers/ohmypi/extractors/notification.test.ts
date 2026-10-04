import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { ohMyPiCompactionBoundary, ohMyPiNotificationEntry } from './notification'

function parsed(parentObject: Record<string, unknown>): ParsedMessageContent {
  return { wrapper: null, topLevel: parentObject, parentObject, rawText: '', supplementalContent: undefined, messageMetadata: undefined }
}

describe('ohMyPiNotificationEntry', () => {
  it('reads the compaction pair', () => {
    // omp 18.2.11's own frames (probe s5), shortened. The end frame states no
    // reason: only the start frame does.
    expect(ohMyPiNotificationEntry({ type: 'auto_compaction_start', reason: 'threshold', action: 'snapcompact' })).toEqual([{ kind: 'compaction', phase: 'start' }])
    expect(ohMyPiNotificationEntry({ type: 'auto_compaction_end', action: 'snapcompact', result: { summary: 's', tokensBefore: 60030 }, aborted: false, willRetry: false })).toEqual([
      { kind: 'compaction', phase: 'end', detail: { trigger: 'auto', pre: 60030 } },
    ])
    expect(ohMyPiNotificationEntry({ type: 'auto_compaction_end', action: 'context-full', aborted: true, willRetry: false })).toEqual([{ kind: 'compaction', phase: 'end', error: 'aborted' }])
    expect(ohMyPiNotificationEntry({ type: 'auto_compaction_end', action: 'context-full', aborted: false, willRetry: false, skipped: true })).toEqual([{ kind: 'status', text: 'Compaction skipped' }])
  })

  it('reads a compaction that failed as a failure, not as a boundary', () => {
    // omp 18.2.11's failed end (`session-maintenance.ts`): no result, and its own sentence.
    expect(ohMyPiNotificationEntry({ type: 'auto_compaction_end', action: 'snapcompact', aborted: false, willRetry: false, errorMessage: 'Auto-compaction failed: 500 upstream error' })).toEqual([
      { kind: 'text', text: 'Auto-compaction failed: 500 upstream error' },
    ])
    expect(ohMyPiNotificationEntry({ type: 'auto_compaction_end', action: 'snapcompact', aborted: false, willRetry: false })).toEqual([
      { kind: 'compaction', phase: 'end', error: 'omp stated no result' },
    ])
  })

  it('reads the answer to the worker\'s compact command', () => {
    expect(ohMyPiNotificationEntry({ type: 'response', id: 'leapmux-4', command: 'compact', success: true, data: { tokensBefore: 1200 } })).toEqual([
      { kind: 'compaction', phase: 'end', detail: { trigger: 'manual', pre: 1200 } },
    ])
    expect(ohMyPiNotificationEntry({ type: 'response', command: 'compact', success: false, error: 'Nothing to compact (session too small)' })).toEqual([
      { kind: 'status', text: 'Compaction skipped: Nothing to compact (session too small)' },
    ])
    expect(ohMyPiNotificationEntry({ type: 'response', command: 'compact', success: false })).toEqual([{ kind: 'status', text: 'Compaction skipped' }])
    expect(ohMyPiNotificationEntry({ type: 'response', command: 'prompt', success: true })).toEqual([])
  })

  it('reads the retries', () => {
    // omp 18.2.11's own frames (probe s8b), shortened.
    expect(ohMyPiNotificationEntry({ type: 'auto_retry_start', attempt: 1, maxAttempts: 10, delayMs: 92.36, errorMessage: '400 bad request' })).toEqual([
      { kind: 'retry', scope: 'api', attempt: 1, maxAttempts: 10, delayMs: 92.36, error: '400 bad request' },
    ])
    expect(ohMyPiNotificationEntry({ type: 'auto_retry_end', success: true, attempt: 1 })).toEqual([{ kind: 'retry', scope: 'api', attempt: 1, succeeded: true }])
    expect(ohMyPiNotificationEntry({ type: 'auto_retry_end', success: false, attempt: 10, finalError: 'gave up' })).toEqual([
      { kind: 'retry', scope: 'api', attempt: 10, willRetry: false, error: 'gave up' },
    ])
  })

  it('reads the fallback, the notices and the errors', () => {
    expect(ohMyPiNotificationEntry({ type: 'retry_fallback_applied', from: 'a/b', to: 'c/d', role: 'default', reason: 'rate limit' })).toEqual([{ kind: 'text', text: 'Switched the model from a/b to c/d (rate limit)' }])
    expect(ohMyPiNotificationEntry({ type: 'retry_fallback_succeeded', model: 'c/d' })).toEqual([{ kind: 'text', text: 'The fallback model c/d answered' }])
    expect(ohMyPiNotificationEntry({ type: 'retry_fallback_succeeded' })).toEqual([{ kind: 'text', text: 'The fallback model answered' }])
    expect(ohMyPiNotificationEntry({ type: 'notice', level: 'info', message: 'The current model has no service-tier control.' })).toEqual([{ kind: 'text', text: 'The current model has no service-tier control.' }])
    expect(ohMyPiNotificationEntry({ type: 'notice', message: '  ' })).toEqual([])
    expect(ohMyPiNotificationEntry({ type: 'extension_error', extensionPath: '/x.ts', event: 'agent_end', error: 'boom' })).toEqual([{ kind: 'text', text: 'Extension error in /x.ts (agent_end): boom' }])
    expect(ohMyPiNotificationEntry({ type: 'command_output', text: 'Current model: mock/mock-model' })).toEqual([{ kind: 'text', text: 'Current model: mock/mock-model' }])
    expect(ohMyPiNotificationEntry({ type: 'command_output', text: '' })).toEqual([])
    expect(ohMyPiNotificationEntry({ type: 'rpc_frame_error', error: 'too large' })).toEqual([{ kind: 'text', text: 'omp could not send a frame: too large' }])
  })

  it('reads a compaction that states no size before it, and a size of zero', () => {
    expect(ohMyPiNotificationEntry({ type: 'auto_compaction_end', result: { summary: 's' } })).toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto' } }])
    expect(ohMyPiNotificationEntry({ type: 'auto_compaction_end', result: { tokensBefore: 0 } })).toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'auto', pre: 0 } }])
    expect(ohMyPiNotificationEntry({ type: 'response', command: 'compact', success: true })).toEqual([{ kind: 'compaction', phase: 'end', detail: { trigger: 'manual' } }])
  })

  it('reads each frame that states none of its optional fields', () => {
    expect(ohMyPiNotificationEntry({ type: 'auto_retry_start' })).toEqual([{ kind: 'retry', scope: 'api' }])
    expect(ohMyPiNotificationEntry({ type: 'auto_retry_end', success: false })).toEqual([{ kind: 'retry', scope: 'api', willRetry: false }])
    // A retry that succeeded states no error, although the frame carries one.
    expect(ohMyPiNotificationEntry({ type: 'auto_retry_end', success: true, finalError: 'stale' })).toEqual([{ kind: 'retry', scope: 'api', succeeded: true }])
    expect(ohMyPiNotificationEntry({ type: 'retry_fallback_applied' })).toEqual([{ kind: 'text', text: 'Switched the model' }])
    expect(ohMyPiNotificationEntry({ type: 'extension_error' })).toEqual([{ kind: 'text', text: 'Extension error' }])
    expect(ohMyPiNotificationEntry({ type: 'rpc_frame_error' })).toEqual([{ kind: 'text', text: 'omp could not send a frame' }])
    expect(ohMyPiNotificationEntry({ type: 'extension_ui_request', method: 'open_url' })).toEqual([])
    expect(ohMyPiNotificationEntry({ type: 'extension_ui_request' })).toEqual([])
  })

  it('reads a to-do reminder', () => {
    expect(ohMyPiNotificationEntry({ type: 'todo_reminder', todos: [{ content: 'Write code', status: 'in_progress' }], attempt: 1, maxAttempts: 3 })).toEqual([
      { kind: 'status', text: '1 to-do item is open; the agent continues (reminder 1 of 3)' },
    ])
    expect(ohMyPiNotificationEntry({ type: 'todo_reminder', todos: [{}, {}] })).toEqual([{ kind: 'status', text: '2 to-do items are open; the agent continues' }])
  })

  it('states the reminder count only when omp states both the attempt and the limit', () => {
    expect(ohMyPiNotificationEntry({ type: 'todo_reminder', todos: [{}], attempt: 2 })).toEqual([{ kind: 'status', text: '1 to-do item is open; the agent continues' }])
    expect(ohMyPiNotificationEntry({ type: 'todo_reminder', todos: [{}], maxAttempts: 3 })).toEqual([{ kind: 'status', text: '1 to-do item is open; the agent continues' }])
  })

  it('reads an agent-to-agent message whose details state no sender or recipient as its body alone', () => {
    const bare = (customType: string, details: Record<string, unknown>) => ohMyPiNotificationEntry({ type: 'irc_message', message: { role: 'custom', customType, content: 'template', details } })
    expect(bare('irc:incoming', { message: 'Hi.' })).toEqual([{ kind: 'text', text: 'Hi.' }])
    expect(bare('irc:autoreply', { body: 'Busy.' })).toEqual([{ kind: 'text', text: 'Busy.' }])
    expect(bare('irc:relay', { from: 'A', body: 'Hi.' })).toEqual([{ kind: 'text', text: 'Hi.' }])
    expect(bare('irc:workpool', { to: 'w1', body: 'Take it.' })).toEqual([{ kind: 'text', text: 'Take it.' }])
    // A body that is blank reads the content instead.
    expect(bare('irc:incoming', { from: 'A', message: '  ' })).toEqual([{ kind: 'text', text: 'template' }])
  })

  it('reads an agent-to-agent message from the sender and the body omp states in its details', () => {
    // omp 18.2.11's records (`irc-bridge.ts`, `irc/bus.ts`, `task/workpool.ts`): the
    // content is a template for the model, and the details state the message.
    const incoming = { role: 'custom', customType: 'irc:incoming', content: '<irc>...</irc>', display: true, details: { id: 'm1', from: 'ScoutOne', message: 'Done with the parser.' }, attribution: 'agent' }
    expect(ohMyPiNotificationEntry({ type: 'irc_message', message: incoming })).toEqual([{ kind: 'text', text: 'Message from ScoutOne: Done with the parser.' }])
    const autoreply = { role: 'custom', customType: 'irc:autoreply', content: '[IRC you → `Main` (auto)]\n\nBusy.', display: true, details: { to: 'Main', body: 'Busy.', replyTo: 'm1' } }
    expect(ohMyPiNotificationEntry({ type: 'irc_message', message: autoreply })).toEqual([{ kind: 'text', text: 'Automatic reply to Main: Busy.' }])
    const relay = { role: 'custom', customType: 'irc:relay', content: '[IRC `A` → `B`]\n\nHi.', display: true, details: { from: 'A', to: 'B', body: 'Hi.' } }
    expect(ohMyPiNotificationEntry({ type: 'irc_message', message: relay })).toEqual([{ kind: 'text', text: 'Message from A to B: Hi.' }])
    const workpool = { role: 'custom', customType: 'irc:workpool', content: '[pool p → w1]\n\nTake item 3.', display: true, details: { pool: 'p', from: 'pool:p', to: 'w1', body: 'Take item 3.', mode: 'dispatched' } }
    expect(ohMyPiNotificationEntry({ type: 'irc_message', message: workpool })).toEqual([{ kind: 'text', text: 'Pool p to w1: Take item 3.' }])
  })

  it('reads an agent-to-agent message whose details state no body from its content, in either shape', () => {
    expect(ohMyPiNotificationEntry({ type: 'irc_message', message: { role: 'custom', content: 'hi' } })).toEqual([{ kind: 'text', text: 'hi' }])
    expect(ohMyPiNotificationEntry({ type: 'irc_message', message: { content: [{ type: 'text', text: 'a' }, { type: 'text', text: 'b' }] } })).toEqual([{ kind: 'text', text: 'a\nb' }])
    expect(ohMyPiNotificationEntry({ type: 'irc_message', message: {} })).toEqual([])
  })

  it('states the severity of an extension notice that is not plain information', () => {
    expect(ohMyPiNotificationEntry({ type: 'extension_ui_request', method: 'notify', message: 'Disk almost full.', notifyType: 'warning' })).toEqual([{ kind: 'text', text: 'Warning: Disk almost full.' }])
    expect(ohMyPiNotificationEntry({ type: 'extension_ui_request', method: 'notify', message: 'Index failed.', notifyType: 'error' })).toEqual([{ kind: 'text', text: 'Error: Index failed.' }])
    expect(ohMyPiNotificationEntry({ type: 'extension_ui_request', method: 'notify', message: 'Indexed.', notifyType: 'info' })).toEqual([{ kind: 'text', text: 'Indexed.' }])
  })

  it('reads an extension notice, a URL and an unknown method', () => {
    expect(ohMyPiNotificationEntry({ type: 'extension_ui_request', method: 'notify', message: 'Indexed.' })).toEqual([{ kind: 'text', text: 'Indexed.' }])
    expect(ohMyPiNotificationEntry({ type: 'extension_ui_request', method: 'notify', message: '' })).toEqual([])
    expect(ohMyPiNotificationEntry({ type: 'extension_ui_request', method: 'open_url', url: 'https://example.com' })).toEqual([{ kind: 'text', text: 'Open https://example.com' }])
    expect(ohMyPiNotificationEntry({ type: 'extension_ui_request', method: 'hologram' })).toEqual([{ kind: 'text', text: 'Extension request: hologram' }])
  })

  it('reads the output of a finished background command', () => {
    // omp 18.2.11's delivery of one job (`async-result.md`): the output is in the
    // content alone, and the details state no output.
    const content = '<system-notice>\nBackground job bash-1 has completed. Resume your work using the result below.\nbuilt 12 files\n\nWall time: 2.1 seconds\n</system-notice>'
    const frame = { type: 'message_end', message: { role: 'custom', customType: 'async-result', content, display: true, details: { jobs: [{ jobId: 'bash-1', type: 'bash', label: 'npm run build', durationMs: 2100 }] } } }
    expect(ohMyPiNotificationEntry(frame)).toEqual([
      { kind: 'subagent-report', label: 'npm run build', text: '```\nbuilt 12 files\n\nWall time: 2.1 seconds\n```' },
    ])
  })

  it('reads each job of a delivery of several, and leaves a subagent\'s report to its own transcript', () => {
    const content = [
      '<system-notice>',
      '2 background jobs have completed. Resume your work using the results below.',
      '',
      '── Job bash-1 (npm test) ──',
      'ok ```fenced``` output',
      '── Job ScoutOne (ScoutOne) ──',
      '<task-result id="ScoutOne" agent="task" status="completed">',
      '<output>',
      'child says hello',
      '</output>',
      '</task-result>',
      '</system-notice>',
    ].join('\n')
    const frame = { type: 'message_end', message: { role: 'custom', customType: 'async-result', content, display: true, details: { jobs: [{ jobId: 'bash-1', type: 'bash', label: 'npm test' }, { jobId: 'ScoutOne', type: 'task', label: 'ScoutOne' }] } } }
    expect(ohMyPiNotificationEntry(frame)).toEqual([
      { kind: 'subagent-report', label: 'npm test', text: '````\nok ```fenced``` output\n````' },
      { kind: 'group', groupKey: 'omp-background-job', prefix: 'Background job finished', entry: 'ScoutOne' },
    ])
  })

  it('states the job alone when the delivery states no output for it', () => {
    const frame = { type: 'message_end', message: { role: 'custom', customType: 'async-result', content: '<system-notice>\n</system-notice>', details: { jobs: [{ jobId: 'bash-2' }] } } }
    expect(ohMyPiNotificationEntry(frame)).toEqual([{ kind: 'group', groupKey: 'omp-background-job', prefix: 'Background job finished', entry: 'bash-2' }])
    const noJobs = { type: 'message_end', message: { role: 'custom', customType: 'async-result', content: 'x' } }
    expect(ohMyPiNotificationEntry(noJobs)).toEqual([{ kind: 'text', text: 'A background job finished' }])
  })

  it('reads a message omp injects for the model as a notice, never as the reply', () => {
    // omp 18.2.11 (`modes/skill-command.ts`): a skill's whole file, attributed to the user.
    const skill = { type: 'message_end', message: { role: 'custom', customType: 'skill-prompt', content: '# Release\n\nStep one...', display: true, details: { name: 'release', path: '/s/release/SKILL.md', args: 'v2' }, attribution: 'user' } }
    expect(ohMyPiNotificationEntry(skill)).toEqual([{ kind: 'text', text: 'Loaded the skill release' }])
    // omp 18.2.11 (`sdk.ts`, `lsp-late-diagnostic.md`): the notice omp words for the model.
    const diagnostics = { type: 'message_end', message: { role: 'custom', customType: 'lsp-late-diagnostic', content: '<system-notice>\nLate LSP diagnostics arrived after the edit returned:\n\nsrc/a.ts — 1 error\n</system-notice>', display: true, details: { files: [] }, attribution: 'agent' } }
    expect(ohMyPiNotificationEntry(diagnostics)).toEqual([{ kind: 'text', text: 'Late LSP diagnostics arrived after the edit returned:\n\nsrc/a.ts — 1 error' }])
    const launch = { type: 'message_end', message: { role: 'custom', customType: 'launch-completion', content: 'Supervised process web exited with exit code 0.', display: true, attribution: 'agent' } }
    expect(ohMyPiNotificationEntry(launch)).toEqual([{ kind: 'text', text: 'Supervised process web exited with exit code 0.' }])
  })

  it('heads a job with its id when it states no label, and with a word of its own when it states neither', () => {
    const content = '<system-notice>\n2 background jobs have completed.\n── Job bash-1 ──\none\n</system-notice>'
    const frame = { type: 'message_end', message: { role: 'custom', customType: 'async-result', content, details: { jobs: [{ jobId: 'bash-1', type: 'bash' }, { type: 'bash' }] } } }
    expect(ohMyPiNotificationEntry(frame)).toEqual([
      { kind: 'subagent-report', label: 'bash-1', text: '```\none\n```' },
      { kind: 'group', groupKey: 'omp-background-job', prefix: 'Background job finished', entry: 'job' },
    ])
  })

  it('reads the job headers of a delivery of several only for the jobs it lists', () => {
    // A line of output that only looks like a header stays in the output of its job.
    const content = '<system-notice>\n2 background jobs have completed.\n── Job a ──\nfirst\n── Job zzz ──\nstill a\n── Job b ──\nsecond\n</system-notice>'
    const frame = { type: 'message_end', message: { role: 'custom', customType: 'async-result', content, details: { jobs: [{ jobId: 'a', type: 'bash', label: 'A' }, { jobId: 'b', type: 'bash', label: 'B' }] } } }
    expect(ohMyPiNotificationEntry(frame)).toEqual([
      { kind: 'subagent-report', label: 'A', text: '```\nfirst\n── Job zzz ──\nstill a\n```' },
      { kind: 'subagent-report', label: 'B', text: '```\nsecond\n```' },
    ])
  })

  it('reads a skill that states no name by its words, and a message that is not custom as nothing', () => {
    const skill = { type: 'message_end', message: { role: 'custom', customType: 'skill-prompt', content: '# Release', details: {} } }
    expect(ohMyPiNotificationEntry(skill)).toEqual([{ kind: 'text', text: '# Release' }])
    expect(ohMyPiNotificationEntry({ type: 'message_end', message: { role: 'assistant', content: 'Hello.' } })).toEqual([])
    expect(ohMyPiNotificationEntry({ type: 'message_end' })).toEqual([])
  })

  it('reads nothing from the copy of an agent-to-agent message that the irc_message frame already states', () => {
    const copy = { type: 'message_end', message: { role: 'custom', customType: 'irc:incoming', content: '<irc>...</irc>', display: true, details: { id: 'm1', from: 'A', message: 'Hi.' } } }
    expect(ohMyPiNotificationEntry(copy)).toEqual([])
  })

  it('states nothing for a frame it does not read', () => {
    expect(ohMyPiNotificationEntry({ type: 'hologram' })).toEqual([])
    expect(ohMyPiNotificationEntry({})).toEqual([])
  })
})

describe('ohMyPiCompactionBoundary', () => {
  it('reads a finished compaction, automatic or requested', () => {
    expect(ohMyPiCompactionBoundary(parsed({ type: 'auto_compaction_end', action: 'context-full', result: { tokensBefore: 90000 }, aborted: false, willRetry: false }))).toEqual({ trigger: 'auto', pre: 90000 })
    expect(ohMyPiCompactionBoundary(parsed({ type: 'response', command: 'compact', success: true, data: { tokensBefore: 5 } }))).toEqual({ trigger: 'manual', pre: 5 })
  })

  it('states no boundary for a compaction that rewrote nothing', () => {
    expect(ohMyPiCompactionBoundary(parsed({ type: 'auto_compaction_end', aborted: true }))).toBeNull()
    expect(ohMyPiCompactionBoundary(parsed({ type: 'auto_compaction_end', skipped: true }))).toBeNull()
    expect(ohMyPiCompactionBoundary(parsed({ type: 'auto_compaction_end', action: 'snapcompact', aborted: false, willRetry: false, errorMessage: 'Auto-compaction failed: boom' }))).toBeNull()
    expect(ohMyPiCompactionBoundary(parsed({ type: 'auto_compaction_end', action: 'snapcompact', aborted: false, willRetry: false }))).toBeNull()
    expect(ohMyPiCompactionBoundary(parsed({ type: 'response', command: 'compact', success: false }))).toBeNull()
    expect(ohMyPiCompactionBoundary(parsed({ type: 'auto_compaction_start' }))).toBeNull()
  })
})
