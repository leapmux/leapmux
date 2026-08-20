import type { MessageCategory } from '../messageClassification'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { claudeToolResultMeta } from './claude/toolResult'
import './testMocks'

const { renderMessageContent } = await import('../messageRenderers')

const PROMPT = 'You are the lead reviewer subagent. Read the code that it names.'
const OUTPUT_FILE = '/private/tmp/claude-501/-Users-trustin/tasks/a7bcba10b2b861663.output'

/**
 * The instructions the CLI attaches to a launch. Addressed to the calling MODEL
 * -- "never quote or paste any part of it", the id it must not mention, the
 * warning against reading the output file -- and about nothing the user needs.
 */
const HARNESS_TEXT = [
  'Async agent launched successfully. (This tool result is internal metadata — never quote or paste any part of it.)',
  'agentId: a7bcba10b2b861663 (internal ID - do not mention to user.)',
  'Do NOT Read or tail this file via the shell tool.',
].join('\n')

function asyncLaunch(over: Record<string, unknown> = {}) {
  return {
    isAsync: true,
    status: 'async_launched',
    agentId: 'a7bcba10b2b861663',
    description: 'Verify V1 theme pair freeze',
    resolvedModel: 'claude-opus-5[1m]',
    prompt: PROMPT,
    outputFile: OUTPUT_FILE,
    canReadOutputFile: true,
    ...over,
  }
}

function agentToolResult(resultContent: string, toolUseResult?: Record<string, unknown>) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{ tool_use_id: 'test-agent', type: 'tool_result', content: resultContent }],
    },
    ...(toolUseResult ? { tool_use_result: toolUseResult } : {}),
  }
}

function renderAgentResult(resultContent: string, toolUseResult?: Record<string, unknown>): HTMLElement {
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(
    agentToolResult(resultContent, toolUseResult),
    { spanType: 'Agent' },
    category,
    AgentProvider.CLAUDE_CODE,
  )
  return render(() => result).container
}

describe('claude Agent tool_result rendering: an async launch', () => {
  // The task title is the only human-written string in the payload, and the one
  // the user recognizes. The agent id is a generated token that says nothing
  // about the work, so it belongs in the field list rather than the headline.
  it('shows the task in the header rather than the agent id', () => {
    const text = renderAgentResult(HARNESS_TEXT, asyncLaunch()).textContent ?? ''
    expect(text).toContain('Agent \'Verify V1 theme pair freeze\' launched asynchronously')
  })

  it('lists the agent id, the model and the output file', () => {
    const text = renderAgentResult(HARNESS_TEXT, asyncLaunch()).textContent ?? ''
    expect(text).toContain('Agent ID:')
    expect(text).toContain('a7bcba10b2b861663')
    expect(text).toContain('Model:')
    expect(text).toContain('claude-opus-5[1m]')
    expect(text).toContain('Output:')
    expect(text).toContain(OUTPUT_FILE)
  })

  it('shows the prompt as the body, under a label', () => {
    const text = renderAgentResult(HARNESS_TEXT, asyncLaunch()).textContent ?? ''
    expect(text).toContain('Prompt')
    expect(text).toContain('Read the code that it names')
  })

  // The whole point of the change: a launch has no report, so the old card fell
  // back to the harness instructions and buried the task under them.
  it('drops the harness instructions entirely', () => {
    const text = renderAgentResult(HARNESS_TEXT, asyncLaunch()).textContent ?? ''
    expect(text).not.toContain('internal metadata')
    expect(text).not.toContain('Do NOT Read or tail')
  })

  // resolvedModel alone reports only where the run ended up, which reads as
  // though it ran there throughout.
  it('reports a mid-run model swap in full', () => {
    const text = renderAgentResult(HARNESS_TEXT, asyncLaunch({
      modelsUsed: ['claude-sonnet-5', 'claude-opus-5[1m]'],
    })).textContent ?? ''
    expect(text).toContain('Models:')
    expect(text).toContain('claude-sonnet-5 → claude-opus-5[1m]')
  })

  // The header is one CLIPPED line that ENDS with the outcome, so an unbounded
  // description pushes `launched asynchronously` off the right edge -- and the
  // icon separates only `completed` from everything else, so a failed run and a
  // launched one become indistinguishable.
  it('caps an overlong task description so the outcome stays in the header', () => {
    const description = 'Audit every renderer that clips a one-line header '.repeat(4)
    const text = renderAgentResult(HARNESS_TEXT, asyncLaunch({ description })).textContent ?? ''
    expect(text).toContain('\u2026\' launched asynchronously')
    expect(text).not.toContain(description)
  })

  it('keeps a multi-line description to its first line', () => {
    const text = renderAgentResult(HARNESS_TEXT, asyncLaunch({
      description: 'Freeze the theme pairs\nand then audit them',
    })).textContent ?? ''
    expect(text).toContain('Agent \'Freeze the theme pairs\' launched asynchronously')
    expect(text).not.toContain('and then audit them')
  })

  // A whitespace-only description took the truthy branch and rendered
  // `Agent '   ' launched asynchronously`; the trim makes it fall back to the id,
  // which is what agentTitle's own doc says happens.
  it('falls back to the agent id for a blank description', () => {
    const text = renderAgentResult(HARNESS_TEXT, asyncLaunch({ description: '   ' })).textContent ?? ''
    expect(text).toContain('Agent a7bcba10b2b861663 launched asynchronously')
  })

  // A finished sync run carries no description at all, so the id is all there is.
  it('falls back to the agent id when the payload carries no description', () => {
    const text = renderAgentResult(HARNESS_TEXT, asyncLaunch({ description: '' })).textContent ?? ''
    expect(text).toContain('Agent a7bcba10b2b861663 launched asynchronously')
  })
})

describe('claude Agent tool_result rendering: a remote launch', () => {
  it('shows the task and lists the task id and session url', () => {
    const text = renderAgentResult('', {
      status: 'remote_launched',
      taskId: 'task-remote-1',
      sessionUrl: 'https://claude.ai/session/abc',
      description: 'Audit the migration',
      prompt: PROMPT,
      outputFile: OUTPUT_FILE,
    }).textContent ?? ''
    expect(text).toContain('Agent \'Audit the migration\' launched remotely')
    expect(text).toContain('Task ID:')
    expect(text).toContain('task-remote-1')
    expect(text).toContain('Session:')
    expect(text).toContain('https://claude.ai/session/abc')
  })
})

describe('claude Agent tool_result rendering: a finished run', () => {
  const completed = {
    status: 'completed',
    agentId: 'a7bcba10b2b861663',
    content: [{ type: 'text', text: 'The tide comes in twice a day.' }],
    prompt: PROMPT,
    resolvedModel: 'claude-opus-5[1m]',
  }

  // The report is the deliverable here, so it stays the body and the prompt does
  // not push it down the card.
  it('shows the report rather than the prompt', () => {
    const text = renderAgentResult('', completed).textContent ?? ''
    expect(text).toContain('The tide comes in twice a day.')
    expect(text).not.toContain('Read the code that it names')
    expect(text).not.toContain('Prompt')
  })

  it('keeps the agent id in the header when there is no description', () => {
    const text = renderAgentResult('', completed).textContent ?? ''
    expect(text).toContain('Agent a7bcba10b2b861663 completed')
  })

  // A sync result recorded before the structured content existed has only the
  // block text, and that IS the report -- unlike a launch, where it is harness
  // instructions.
  it('falls back to the block content for a run that carries no content array', () => {
    const text = renderAgentResult('A plain report.', {
      status: 'completed',
      agentId: 'a1',
    }).textContent ?? ''
    expect(text).toContain('A plain report.')
  })

  it('falls through to the catch-all when there is no structured payload at all', () => {
    const text = renderAgentResult('just text').textContent ?? ''
    expect(text).not.toContain('Agent ID:')
  })
})

describe('claudeToolResultMeta for Agent', () => {
  const meta = (toolUseResult: Record<string, unknown>, resultContent = HARNESS_TEXT) =>
    claudeToolResultMeta(
      { kind: 'tool_result' },
      agentToolResult(resultContent, toolUseResult),
      'Agent',
      undefined,
    )

  // The toolbar must act on the text the card shows. Copying the harness
  // instructions gave the user the one thing on the row that is not about their
  // subagent.
  it('copies the prompt for a launch, not the harness instructions', () => {
    expect(meta(asyncLaunch())?.copyableContent()).toBe(PROMPT)
  })

  it('copies the report for a finished run', () => {
    const copied = meta({
      status: 'completed',
      agentId: 'a1',
      content: [{ type: 'text', text: 'The report.' }],
      prompt: PROMPT,
    }, '')?.copyableContent()
    expect(copied).toBe('The report.')
  })

  it('judges collapsibility from the body the card renders', () => {
    const longPrompt = Array.from({ length: 40 }, (_, i) => `line ${i}`).join('\n')
    expect(meta(asyncLaunch({ prompt: longPrompt }))?.collapsible).toBe(true)
    expect(meta(asyncLaunch({ prompt: 'one line' }))?.collapsible).toBe(false)
  })
})

describe('claude Agent tool_result rendering: field-list edge cases', () => {
  // One entry is not a swap, so the singular label is the honest one.
  it('reports a single-entry modelsUsed as the plain model', () => {
    const text = renderAgentResult(HARNESS_TEXT, asyncLaunch({
      modelsUsed: ['claude-opus-5[1m]'],
    })).textContent ?? ''
    expect(text).toContain('Model:')
    expect(text).not.toContain('Models:')
  })

  it('lists the worktree of an isolated run', () => {
    const text = renderAgentResult('', {
      status: 'completed',
      agentId: 'a1',
      content: [{ type: 'text', text: 'done' }],
      worktreePath: '/tmp/wt/feature',
      worktreeBranch: 'feature-x',
    }).textContent ?? ''
    expect(text).toContain('Branch:')
    expect(text).toContain('feature-x')
    expect(text).toContain('Worktree:')
    expect(text).toContain('/tmp/wt/feature')
  })

  // Neither a report nor a prompt: the card is its header and fields, with no
  // empty body block below them.
  it('renders no body when the payload carries neither report nor prompt', () => {
    const container = renderAgentResult('', {
      status: 'async_launched',
      agentId: 'a1',
      description: 'Something',
    })
    expect(container.textContent).toContain('Agent \'Something\' launched asynchronously')
    expect(container.textContent).not.toContain('Prompt')
  })

  // A launch whose payload lost its agent id still says what it is, rather than
  // rendering a header with a gap where the identity should be.
  it('keeps a readable header when the launch carries no identity at all', () => {
    const text = renderAgentResult('', {
      status: 'async_launched',
      prompt: 'do the thing',
    }).textContent ?? ''
    expect(text).toContain('Agent launched asynchronously')
  })
})
