import type { MessageCategory } from '../messageClassifier'
import type { RenderContext } from '../messageRenderers'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolMeta } from '~/test-support/toolCallFixture'
import './testMocks'

const { renderMessageContent } = await import('../messageContentRenderer')

/** Build a Claude `RemoteTrigger` tool_use assistant message. */
function makeRemoteTriggerToolUse(input: Record<string, unknown>) {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'test-remotetrigger',
        name: 'RemoteTrigger',
        input,
      }],
    },
  }
}

/** Build a Claude `RemoteTrigger` tool_result user message. */
function makeRemoteTriggerToolResult(
  resultContent: string,
  toolUseResult?: Record<string, unknown>,
) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{
        tool_use_id: 'test-remotetrigger',
        type: 'tool_result',
        content: resultContent,
      }],
    },
    ...(toolUseResult ? { tool_use_result: toolUseResult } : {}),
  }
}

function renderToolUseText(input: Record<string, unknown>): string {
  const msg = makeRemoteTriggerToolUse(input)
  const category: MessageCategory = { kind: 'tool_use' }
  const result = renderMessageContent(msg, undefined, category, AgentProvider.CLAUDE_CODE)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

function renderToolResultContainer(
  resultContent: string,
  toolUseResult?: Record<string, unknown>,
  context?: RenderContext,
): HTMLElement {
  const msg = makeRemoteTriggerToolResult(resultContent, toolUseResult)
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(
    msg,
    { spanType: 'RemoteTrigger', ...context },
    category,
    AgentProvider.CLAUDE_CODE,
  )
  const { container } = render(() => result)
  return container
}

describe('claude RemoteTrigger tool_use rendering', () => {
  it('renders "List triggers" for the list action', () => {
    expect(renderToolUseText({ action: 'list' })).toContain('List triggers')
  })

  it('renders "Get trigger {id}" for the get action', () => {
    expect(renderToolUseText({ action: 'get', trigger_id: 'trig_abc' }))
      .toContain('Get trigger trig_abc')
  })

  it('renders "Create trigger: {name}" for the create action when body.name is present', () => {
    expect(renderToolUseText({
      action: 'create',
      body: { name: 'Nightly cleanup' },
    })).toContain('Create trigger: Nightly cleanup')
  })

  it('renders "Update trigger {id}: {name}" for the update action', () => {
    expect(renderToolUseText({
      action: 'update',
      trigger_id: 'trig_xyz',
      body: { name: 'Renamed task' },
    })).toContain('Update trigger trig_xyz: Renamed task')
  })

  it('renders "Run trigger {id}" for the run action', () => {
    expect(renderToolUseText({ action: 'run', trigger_id: 'trig_run' }))
      .toContain('Run trigger trig_run')
  })

  it('falls back to plain "RemoteTrigger" when action is missing', () => {
    expect(renderToolUseText({})).toContain('RemoteTrigger')
  })

  // Claude reads its trigger request through the shared entry, which accepts two more
  // spellings of the id (`triggerId` and `id`) and a ROOT-level `name`. `RemoteTrigger`
  // sends `trigger_id` and `body.name` instead, so no transcript changes today. The two
  // cases below pin what the row draws if one of the wider spellings does arrive.
  it('renders the id a call spells `id`, which the shared reading also accepts', () => {
    expect(renderToolUseText({ action: 'get', id: 'trig_shared' }))
      .toContain('Get trigger trig_shared')
  })

  it('draws the body name ahead of a root name when a call carries both', () => {
    expect(renderToolUseText({
      action: 'create',
      name: 'From the root',
      body: { name: 'From the body' },
    })).toContain('Create trigger: From the body')
  })
})

describe('claude RemoteTrigger tool_result rendering', () => {
  const triggerJson = JSON.stringify({
    trigger: {
      id: 'trig_012pVbRPB8r3wTzhQGomdhBf',
      name: 'Remove FORCE_JAVASCRIPT_ACTIONS_TO_NODE24 env override',
      enabled: true,
      next_run_at: '2026-06-10T00:00:00Z',
    },
  })

  it('renders HTTP status, trigger name, and trigger id from tool_use_result', () => {
    const container = renderToolResultContainer(
      `HTTP 200\n${triggerJson}`,
      { status: 200, json: triggerJson },
    )
    const text = container.textContent ?? ''
    expect(text).toContain('HTTP 200')
    expect(text).toContain('Remove FORCE_JAVASCRIPT_ACTIONS_TO_NODE24 env override')
    expect(text).toContain('trig_012pVbRPB8r3wTzhQGomdhBf')
  })

  it('falls back to parsing the literal HTTP {status}\\n{json} content when tool_use_result is absent', () => {
    const container = renderToolResultContainer(`HTTP 201\n${triggerJson}`)
    const text = container.textContent ?? ''
    expect(text).toContain('HTTP 201')
    expect(text).toContain('Remove FORCE_JAVASCRIPT_ACTIONS_TO_NODE24 env override')
  })

  it('renders only HTTP status when no trigger fields are present', () => {
    const errorBody = '{"error":"Internal Server Error"}'
    const container = renderToolResultContainer(
      `HTTP 500\n${errorBody}`,
      { status: 500, json: errorBody },
    )
    const text = container.textContent ?? ''
    expect(text).toContain('HTTP 500')
    expect(text).toContain('Internal Server Error')
  })

  it('falls through to the catch-all when no recognizable shape is present', () => {
    const container = renderToolResultContainer('opaque non-http output')
    expect(container.textContent ?? '').toContain('opaque non-http output')
  })
})

// A RemoteTrigger result draws a STATUS body: the HTTP status and the trigger it
// names become the header, and the response body becomes the note below it. Both
// answers below follow from that, and both changed when the row moved onto the
// shared model:
//
//   - Collapsibility counts the NOTE's lines, like every other body. The rule it
//     replaced answered true for any structured payload, so a one-line `{}` drew an
//     Expand button that revealed nothing.
//   - The copy text is the note. The `HTTP 200` line it used to start with is the
//     header the row already draws, and no status row copies its own header.
describe('claude toolbar actions for RemoteTrigger', () => {
  it('offers no expand for a response the collapsed row already shows whole', () => {
    const meta = providerToolMeta(AgentProvider.CLAUDE_CODE, {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: 'HTTP 200\n{}' }] },
      tool_use_result: { status: 200, json: '{}' },
    }, { spanType: 'RemoteTrigger' })
    expect(meta?.collapsible).toBe(false)
  })

  it('marks a response longer than the collapsed row as collapsible', () => {
    const compact = '{"trigger":{"id":"trig_1","name":"hello","enabled":true}}'
    const meta = providerToolMeta(AgentProvider.CLAUDE_CODE, {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: `HTTP 200\n${compact}` }] },
      tool_use_result: { status: 200, json: compact },
    }, { spanType: 'RemoteTrigger' })
    expect(meta?.collapsible).toBe(true)
  })

  it('marks a long fallback HTTP {n}\\n... response as collapsible', () => {
    const compact = '{"trigger":{"id":"trig_2","name":"hello","enabled":true}}'
    const meta = providerToolMeta(AgentProvider.CLAUDE_CODE, {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: `HTTP 200\n${compact}` }] },
    }, { spanType: 'RemoteTrigger' })
    expect(meta?.collapsible).toBe(true)
  })

  it('does not mark non-HTTP plain text as collapsible', () => {
    const meta = providerToolMeta(AgentProvider.CLAUDE_CODE, {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: 'opaque' }] },
    }, { spanType: 'RemoteTrigger' })
    expect(meta?.collapsible).toBe(false)
  })

  it('returns prettified JSON for the copy button', () => {
    const compact = '{"trigger":{"id":"trig_1","name":"hello","enabled":true}}'
    const meta = providerToolMeta(AgentProvider.CLAUDE_CODE, {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: `HTTP 200\n${compact}` }] },
      tool_use_result: { status: 200, json: compact },
    }, { spanType: 'RemoteTrigger' })
    const copied = meta?.copyableContent()
    expect(copied).not.toBeNull()
    // Pretty-printed JSON spans multiple lines, unlike the compact source.
    expect(copied!.split('\n').length).toBeGreaterThan(2)
    expect(copied).toContain('"trigger"')
    expect(copied).toContain('"name": "hello"')
  })

  it('prettifies copy text from fallback HTTP {n}\\n... content when tool_use_result is absent', () => {
    const compact = '{"trigger":{"id":"trig_2"}}'
    const meta = providerToolMeta(AgentProvider.CLAUDE_CODE, {
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', content: `HTTP 201\n${compact}` }] },
    }, { spanType: 'RemoteTrigger' })
    const copied = meta?.copyableContent()
    expect(copied).not.toBeNull()
    expect(copied!.split('\n').length).toBeGreaterThan(2)
    expect(copied).toContain('"trig_2"')
  })
})
