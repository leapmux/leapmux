import { describe, expect, it } from 'vitest'
import { acpToolFacts } from '../../acp/extractors/toolCall'
import { grokAgentRequest, grokAgentRun, grokWorkflowRequest, grokWorkflowRun } from './agent'

/** The facts of one finished Grok call, with its text and its `rawOutput` record. */
function finished(text: string, rawOutput?: unknown, status = 'completed') {
  return acpToolFacts({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'call',
    status,
    content: [{ type: 'content', content: { type: 'text', text } }],
    ...(rawOutput !== undefined ? { rawOutput } : {}),
  })
}

describe('grokAgentRequest', () => {
  it('states every launch fact that carries a value', () => {
    expect(grokAgentRequest({
      description: 'Review',
      prompt: 'Look at the diff',
      run_in_background: true,
      model: 'grok-4',
      isolation: 'worktree',
      cwd: '/w/sub',
      resume_from: 'sub-0',
    }, 'Title', 'call-1')).toEqual({
      description: 'Review',
      prompt: 'Look at the diff',
      metadata: [
        { label: 'Background', value: 'Yes' },
        { label: 'Model', value: 'grok-4' },
        { label: 'Isolation', value: 'worktree' },
        { label: 'Working directory', value: '/w/sub' },
        { label: 'Resumes', value: 'sub-0' },
      ],
      registryKey: 'call-1',
    })
  })

  it('reads the resumed subagent from task_id when resume_from is absent', () => {
    expect(grokAgentRequest({ prompt: 'p', task_id: 'sub-9' }, 'T', 'c').metadata).toEqual([{ label: 'Resumes', value: 'sub-9' }])
  })

  // `false` and an absent flag both mean a foreground run, and the row states nothing.
  it('states no background entry for a foreground run', () => {
    expect(grokAgentRequest({ prompt: 'p', background: false, run_in_background: false }, 'T', 'c').metadata).toBeUndefined()
  })

  it('takes the title when the launch states no description, and omits what it lacks', () => {
    expect(grokAgentRequest({}, 'List files', '')).toEqual({ description: 'List files', prompt: '' })
  })
})

describe('grokAgentRun', () => {
  const request = { description: 'List files', prompt: 'p', registryKey: 'call-1' }

  it('reads a foreground run from its record', () => {
    const run = grokAgentRun(finished('Done.', {
      type: 'SubagentCompleted',
      output: 'The **report**',
      subagent_id: 'sub-1',
      subagent_type: 'general-purpose',
      tool_calls: 3,
      turns: 2,
      duration_ms: 1500,
      worktree_path: '/w/.worktrees/sub-1',
    }), request)
    expect(run).toEqual({
      description: 'List files',
      registryKey: 'call-1',
      agentId: 'sub-1',
      outcome: 'completed',
      metadata: [
        { label: 'Agent ID', value: 'sub-1' },
        { label: 'Type', value: 'general-purpose' },
        { label: 'Tool calls', value: '3' },
        { label: 'Turns', value: '2' },
        { label: 'Duration', value: '1.5s' },
        { label: 'Worktree', value: '/w/.worktrees/sub-1' },
      ],
      body: 'The **report**',
    })
  })

  // A zero is a count that Grok stated, so the row keeps it; only an absent count goes.
  it('keeps a zero count and a zero duration, and drops an absent one', () => {
    const run = grokAgentRun(finished('Done.', { type: 'SubagentCompleted', output: '', subagent_id: 'sub-1', tool_calls: 0, duration_ms: 0 }), request)
    expect(run.metadata).toEqual([
      { label: 'Agent ID', value: 'sub-1' },
      { label: 'Tool calls', value: '0' },
      { label: 'Duration', value: '0.0s' },
    ])
  })

  it('reads a background launch as a run that still goes on', () => {
    const text = 'Subagent started in background.\nsubagent_id: sub-2\ndescription: Say done'
    expect(grokAgentRun(finished(text, { type: 'Text', text }), request)).toEqual({
      description: 'List files',
      registryKey: 'call-1',
      agentId: 'sub-2',
      outcome: 'running',
      statusLabel: 'running in the background',
      metadata: [{ label: 'Agent ID', value: 'sub-2' }],
      body: text,
    })
  })

  // The id line must hold the id alone: a line that only mentions the key is prose.
  it('states no outcome for an answer that is neither record nor launch', () => {
    const run = grokAgentRun(finished('The subagent_id: field was empty, so nothing ran.'), request)
    expect(run).toMatchObject({ agentId: '', outcome: 'unknown', metadata: [] })
  })

  it('keeps the outcome of a failed or stopped call whatever its record says', () => {
    const record = { type: 'SubagentCompleted', output: 'Old report', subagent_id: 'sub-1' }
    expect(grokAgentRun(finished('Boom', record, 'failed'), request)).toEqual({ description: 'List files', registryKey: 'call-1', agentId: '', outcome: 'failed', metadata: [], body: 'Boom' })
    expect(grokAgentRun(finished('Stopped', record, 'cancelled'), request)).toMatchObject({ agentId: '', outcome: 'stopped', body: 'Stopped' })
  })

  it('carries no registry key when the launch had none', () => {
    const run = grokAgentRun(finished('x'), { description: 'd', prompt: '' })
    expect(Object.hasOwn(run, 'registryKey')).toBe(false)
  })
})

describe('grokWorkflowRequest', () => {
  it('states the saved workflow, its script and its arguments', () => {
    const facts = finished('started', { type: 'Workflow', name: 'review-changes', script_path: '/s/review.js', run_id: 'wf-1' })
    expect(grokWorkflowRequest({ source: 'await agent("x")', args: { depth: 2 }, validate_only: true }, facts)).toEqual({
      description: 'review-changes',
      prompt: 'await agent("x")',
      promptLabel: 'Script',
      promptFormat: 'pre',
      metadata: [
        { label: 'Script', value: '/s/review.js' },
        { label: 'Arguments', value: '{"depth":2}' },
        { label: 'Validate only', value: 'Yes' },
      ],
    })
  })

  it('states string arguments as they are, and no entry for null arguments', () => {
    const facts = finished('started', { type: 'Workflow', run_id: 'wf-1' })
    expect(grokWorkflowRequest({ args: 'a b' }, facts).metadata).toEqual([{ label: 'Arguments', value: 'a b' }])
    expect(grokWorkflowRequest({ args: null }, facts).metadata).toBeUndefined()
  })

  it('titles a run whose record states no name', () => {
    expect(grokWorkflowRequest({}, finished('started')).description).toBe('Run workflow')
  })
})

describe('grokWorkflowRun', () => {
  const request = { description: 'review-changes', prompt: '' }

  it('reads a started run as running', () => {
    const facts = finished('text', { type: 'Workflow', run_id: 'wf-1', message: 'Workflow review-changes started.' })
    expect(grokWorkflowRun(facts, request, {})).toEqual({
      description: 'review-changes',
      agentId: 'wf-1',
      outcome: 'running',
      metadata: [{ label: 'Run ID', value: 'wf-1' }],
      body: 'Workflow review-changes started.',
    })
  })

  // A validation runs no subagent, so the call's end is the run's end.
  it('reads a validation as completed, even with a run id', () => {
    const facts = finished('Valid.', { type: 'Workflow', run_id: 'wf-1' })
    expect(grokWorkflowRun(facts, request, { validate_only: true })).toMatchObject({ outcome: 'completed', agentId: 'wf-1', body: 'Valid.' })
  })

  it('reads a call with no run id as completed, with the text as its body', () => {
    expect(grokWorkflowRun(finished('Nothing to run.'), request, {})).toEqual({ description: 'review-changes', agentId: '', outcome: 'completed', metadata: [], body: 'Nothing to run.' })
  })

  it('keeps the outcome of a failed or stopped call', () => {
    const record = { type: 'Workflow', run_id: 'wf-1' }
    expect(grokWorkflowRun(finished('Syntax error', record, 'failed'), request, {})).toEqual({ description: 'review-changes', agentId: '', outcome: 'failed', metadata: [], body: 'Syntax error' })
    expect(grokWorkflowRun(finished('Stopped', record, 'cancelled'), request, {})).toMatchObject({ outcome: 'stopped', agentId: '' })
  })
})
