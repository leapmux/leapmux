import type { MiMoToolPart } from './toolCommon'
import { describe, expect, it } from 'vitest'
import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { mimoActorAction, mimoActorOperation, mimoActorRequest, mimoActorRun, mimoActorSpawns, mimoActorTaskOutcome } from './agent'

/** One finished `actor` call. */
function actorPart(fields: Partial<MiMoToolPart>): MiMoToolPart {
  return {
    callId: 'call-1',
    tool: MIMO_TOOL.Actor,
    status: 'completed',
    input: {},
    output: '',
    error: '',
    title: '',
    metadata: {},
    attachments: [],
    ...fields,
  }
}

const spawn = { operation: { action: 'spawn', subagent_type: 'general', description: 'Helper', prompt: 'Work.' } }
const run = { operation: { ...spawn.operation, action: 'run' } }
const RUN_LINE = 'actor_id: explore-1 (to give this subagent more work, `send` it another message)'

/** A blocking run's answer, with the status and the summary its wrapper states. */
function runOutput(status: string, summary?: string, body = 'The answer is 42.'): string {
  return `${RUN_LINE}\n\n<actor_result status="${status}"${summary === undefined ? '' : ` summary="${summary}"`}>\n${body}\n</actor_result>`
}

describe('mimoActorOperation', () => {
  it('reads the operation object', () => {
    expect(mimoActorOperation(spawn)).toEqual(spawn.operation)
  })

  // MiMo refuses an operation sent as a JSON string, so the call states none.
  it.each([
    ['a JSON string', { operation: '{"action":"spawn"}' }],
    ['no operation', {}],
    ['a list', { operation: [] }],
  ])('reads no operation from %s', (_name, input) => {
    expect(mimoActorOperation(input)).toBeUndefined()
    expect(mimoActorAction(input)).toBe('')
    expect(mimoActorSpawns(input)).toBe(false)
  })
})

describe('mimoActorSpawns', () => {
  it.each([
    ['spawn', true],
    ['run', true],
    ['send', false],
    ['status', false],
    ['wait', false],
    ['cancel', false],
    ['models', false],
  ])('answers %s with %s', (action, spawns) => {
    expect(mimoActorSpawns({ operation: { action } })).toBe(spawns)
  })
})

describe('mimoActorRequest', () => {
  it('reads the launch', () => {
    expect(mimoActorRequest(spawn)).toEqual({ description: 'Helper', agentType: 'general', prompt: 'Work.' })
  })

  it('states no agent type that the call gave none', () => {
    expect(mimoActorRequest({ operation: { action: 'spawn', description: 'Helper', prompt: 'Work.' } })).toEqual({ description: 'Helper', prompt: 'Work.' })
    expect(mimoActorRequest({})).toEqual({ description: '', prompt: '' })
  })
})

describe('mimoActorRun', () => {
  it('reads a background spawn, and the id from its text when the metadata states none', () => {
    const started = actorPart({ input: spawn, output: 'Background sub-session started. actor_id: general-1\nThe result will be delivered as a notification when complete.' })
    expect(mimoActorRun(started)).toEqual({
      description: 'Helper',
      agentId: 'general-1',
      statusLabel: 'launched in the background',
      outcome: 'running',
      metadata: [{ label: 'Agent ID', value: 'general-1' }, { label: 'Agent', value: 'general' }],
      body: 'Work.',
      bodyLabel: 'Prompt',
    })
  })

  it('prefers the id and states the model that the metadata gives', () => {
    const started = actorPart({
      input: spawn,
      output: 'Background sub-session started. actor_id: general-1',
      metadata: { actorId: 'general-2', model: { modelID: 'alpha', providerID: 'mock' } },
    })
    expect(mimoActorRun(started)?.metadata).toEqual([
      { label: 'Agent ID', value: 'general-2' },
      { label: 'Agent', value: 'general' },
      { label: 'Model', value: 'alpha' },
    ])
  })

  it('reads a blocking run\'s report and its summary', () => {
    expect(mimoActorRun(actorPart({ input: run, output: runOutput('success', 'Found it') }))).toEqual({
      description: 'Helper',
      agentId: 'explore-1',
      statusLabel: 'success',
      outcome: 'completed',
      metadata: [{ label: 'Agent ID', value: 'explore-1' }, { label: 'Agent', value: 'general' }, { label: 'Summary', value: 'Found it' }],
      body: 'The answer is 42.',
    })
  })

  // The subagent reports success, partial, blocked or failed itself. MiMo writes
  // `cancelled` for a run that an abort stopped, `timeout` for a wait that ended
  // while the subagent still worked, and `unknown` for a report with no status.
  it.each([
    ['success', 'completed'],
    ['partial', 'completed'],
    ['blocked', 'completed'],
    ['unknown', 'completed'],
    ['failed', 'failed'],
    ['cancelled', 'stopped'],
    ['timeout', 'running'],
  ])('reads a run that states %s as %s', (status, outcome) => {
    expect(mimoActorRun(actorPart({ input: run, output: runOutput(status) }))).toMatchObject({ statusLabel: status, outcome })
  })

  it('keeps a report of several lines whole', () => {
    expect(mimoActorRun(actorPart({ input: run, output: runOutput('success', undefined, '**Status**: success\n\nLine one.\nLine two.') }))?.body)
      .toBe('**Status**: success\n\nLine one.\nLine two.')
  })

  // A run whose task_id names no task still runs. MiMo states why in front of the
  // report, and the note stays beside the report rather than turning it into text.
  it('reads a run after a task notice, and states the notice', () => {
    const note = 'note: task_id "T9" does not exist in this session; ran ad-hoc. Create it with the `task` tool first, or omit task_id.'
    const parsed = mimoActorRun(actorPart({ input: run, output: `${note}\n\n${runOutput('success')}` }))
    expect(parsed).toMatchObject({ agentId: 'explore-1', outcome: 'completed', body: 'The answer is 42.' })
    expect(parsed?.metadata).toEqual([{ label: 'Agent ID', value: 'explore-1' }, { label: 'Agent', value: 'general' }, { label: 'Note', value: note }])
  })

  it('reads a background spawn after a task notice, and states the notice', () => {
    const note = 'note: task_id "x" is not a valid task ID (expected Tn or Tn.m); ran ad-hoc. Task IDs come from the `task` tool.'
    const parsed = mimoActorRun(actorPart({ input: spawn, output: `${note}\nBackground sub-session started. actor_id: general-1` }))
    expect(parsed?.metadata).toContainEqual({ label: 'Note', value: note })
    expect(parsed?.agentId).toBe('general-1')
  })

  it.each([
    ['text of neither form', 'The subagent is busy.'],
    ['a run line with no report', RUN_LINE],
    ['a report with no run line', '<actor_result status="success">\nx\n</actor_result>'],
    ['a run line after the report', `<actor_result status="success">\nx\n</actor_result>\n${RUN_LINE}`],
    ['an empty answer', ''],
  ])('reads no run from %s', (_name, output) => {
    expect(mimoActorRun(actorPart({ input: run, output }))).toBeNull()
  })

  // A report can hold any text. The call's own action says which answer it gave, so
  // a report that quotes a spawn's line stays a report.
  it('reads a run whose report quotes the line of a background spawn as a run', () => {
    const parsed = mimoActorRun(actorPart({ input: run, output: runOutput('success', undefined, 'Background sub-session started. actor_id: general-9') }))
    expect(parsed).toMatchObject({ agentId: 'explore-1', outcome: 'completed', body: 'Background sub-session started. actor_id: general-9' })
  })

  it('reads no run from a spawn that answered in the form of a run', () => {
    expect(mimoActorRun(actorPart({ input: spawn, output: runOutput('success') }))).toBeNull()
  })

  // Only a spawn and a run start a subagent. Another action that quotes a spawn's
  // line in its answer reports no run.
  it.each([
    ['a send', { operation: { action: 'send', to_actor_id: 'general-1', content: 'hi' } }],
    ['a status', { operation: { action: 'status', actor_id: 'general-1' } }],
    ['no operation', {}],
  ])('reads no run from %s', (_name, input) => {
    expect(mimoActorRun(actorPart({ input, output: 'Background sub-session started. actor_id: general-1' }))).toBeNull()
  })

  it('prefers the id the metadata gives for a blocking run', () => {
    const parsed = mimoActorRun(actorPart({ input: run, output: runOutput('success'), metadata: { actorId: 'explore-7' } }))
    expect(parsed?.agentId).toBe('explore-7')
    expect(parsed?.metadata[0]).toEqual({ label: 'Agent ID', value: 'explore-7' })
  })

  // A report whose wrapper states an empty status has no word of MiMo's to state,
  // and it claims no failure that MiMo did not state.
  it('reads a run whose wrapper states an empty status as completed, with no status word', () => {
    const parsed = mimoActorRun(actorPart({ input: run, output: runOutput('') }))
    expect(parsed).toMatchObject({ agentId: 'explore-1', outcome: 'completed', body: 'The answer is 42.' })
    expect(parsed).not.toHaveProperty('statusLabel')
  })

  it('states no agent row for a launch that gave no subagent type', () => {
    const bare = { operation: { action: 'spawn', description: 'Helper', prompt: 'Work.' } }
    expect(mimoActorRun(actorPart({ input: bare, output: 'Background sub-session started. actor_id: general-1' }))?.metadata).toEqual([{ label: 'Agent ID', value: 'general-1' }])
  })
})

describe('mimoActorTaskOutcome', () => {
  const outcome = (metadata: Record<string, unknown>, output = '') => mimoActorTaskOutcome(actorPart({ metadata, output }))

  it.each([
    ['running', 'running'],
    ['pending', 'running'],
    ['timeout', 'running'],
    ['cancelled', 'stopped'],
    ['unknown', 'failed'],
  ])('reads the status %s as %s', (status, expected) => {
    expect(outcome({ status })).toBe(expected)
  })

  it.each([
    ['success', 'completed'],
    ['failure', 'failed'],
    ['cancelled', 'stopped'],
  ])('reads an idle subagent whose last outcome is %s as %s', (lastOutcome, expected) => {
    expect(outcome({ status: 'idle', lastOutcome })).toBe(expected)
  })

  // The status snapshot states no last outcome, and MiMo keeps the error of a turn
  // for a failure alone.
  it('reads the last outcome or the error from the answer when the metadata states neither', () => {
    expect(outcome({ status: 'idle' }, JSON.stringify({ status: 'idle', lastOutcome: 'failure' }))).toBe('failed')
    expect(outcome({ status: 'idle' }, JSON.stringify({ status: 'idle', error: 'model refused' }))).toBe('failed')
    expect(outcome({ status: 'idle' }, JSON.stringify({ status: 'idle', error: '' }))).toBe('completed')
    expect(outcome({ status: 'idle' }, JSON.stringify(['idle']))).toBe('completed')
    expect(outcome({ status: 'idle' }, '{broken')).toBe('completed')
  })

  it('prefers the last outcome of the metadata to the answer', () => {
    expect(outcome({ status: 'idle', lastOutcome: 'success' }, JSON.stringify({ lastOutcome: 'failure' }))).toBe('completed')
  })

  // A last outcome of a later release is no outcome this build reads, so the error
  // the snapshot states decides.
  it('reads the error of the snapshot when the last outcome is a word it does not know', () => {
    expect(outcome({ status: 'idle', lastOutcome: 'paused' }, JSON.stringify({ error: 'model refused' }))).toBe('failed')
    expect(outcome({ status: 'idle', lastOutcome: 'paused' })).toBe('completed')
  })

  it('reads an answer that states no status as completed', () => {
    expect(outcome({})).toBe('completed')
    expect(outcome({ status: 'a word from a later release' })).toBe('completed')
  })
})
