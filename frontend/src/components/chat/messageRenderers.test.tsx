import type { MessageCategory } from './messageClassifier'
import type { MessageContentRenderContext } from './messageContentRenderer'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { render } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider, ContentCompression, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { assembledMessageRow } from '~/test-support/assembledMessages'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from './messageContentRenderer'
import { MESSAGE_UI_KEY } from './messageUiKeys'
import { resolveMessageForRendering } from './providers/registry'
import './providers'

// Mock shiki worker to avoid Web Worker unavailability in test environment.
// Vitest auto-hoists vi.mock calls above imports.
vi.mock('~/lib/shikiWorkerClient', () => ({
  tokenizeAsync: vi.fn().mockResolvedValue(null),
}))

/** Build a tool_use assistant message for the given tool name and input. */
function makeToolUseMessage(name: string, input: Record<string, unknown>) {
  return {
    type: 'assistant',
    message: {
      content: [{ type: 'tool_use', id: 'test-id', name, input }],
    },
  }
}

/** Render a tool_use message and return the trimmed text content. */
function renderToolUseText(name: string, input: Record<string, unknown>, context?: MessageContentRenderContext): string {
  const parsed = makeToolUseMessage(name, input)
  const category = { kind: 'tool_use' } as MessageCategory
  const result = renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

describe('skill renderer', () => {
  it('renders Skill: /create-pr', () => {
    expect(renderToolUseText('Skill', { skill: 'create-pr' })).toBe('Skill: /create-pr')
  })

  it('renders Skill: /review-pr', () => {
    expect(renderToolUseText('Skill', { skill: 'review-pr' })).toBe('Skill: /review-pr')
  })

  it('renders Skill: /commit', () => {
    expect(renderToolUseText('Skill', { skill: 'commit' })).toBe('Skill: /commit')
  })
})

describe('agent/task renderer', () => {
  it('renders Agent with description only', () => {
    expect(renderToolUseText('Agent', { description: 'Search codebase' }))
      .toBe('Search codebase')
  })

  it('renders Task with description only', () => {
    expect(renderToolUseText('Task', { description: 'Run tests' }))
      .toBe('Run tests')
  })

  it('renders Agent with description and subagent_type', () => {
    expect(renderToolUseText('Agent', { description: 'Search', subagent_type: 'Explore' }))
      .toBe('Search (Explore)')
  })

  // `Task` is the older name of the same tool, and `canonicalClaudeToolName`
  // folds it at the extraction boundary -- so both spellings state the one name
  // every table below keys on.
  it('falls back to the shared launch word when description is missing', () => {
    // `Task` folds to the canonical `Agent` name first; the description is what
    // titles the row, and the one fallback word every provider shares is `Task`.
    expect(renderToolUseText('Agent', {})).toContain('Task')
    expect(renderToolUseText('Task', {})).toContain('Task')
  })

  it('shows no status (child data fields removed)', () => {
    const text = renderToolUseText('Agent', { description: 'Analyze code' })
    expect(text).toContain('Analyze code')
    expect(text).not.toContain('Running')
    expect(text).not.toContain('Complete')
    expect(text).not.toContain('Failed')
  })

  it('renders description + subagent_type in title without status', () => {
    const text = renderToolUseText('Agent', { description: 'Fix bug', subagent_type: 'code' })
    expect(text).toContain('Fix bug (code)')
    expect(text).not.toContain('Complete')
  })

  it('formats title as "SubAgent: rest" when description starts with subagent name', () => {
    const text = renderToolUseText('Agent', { description: 'Explore message classification', subagent_type: 'Explore' })
    expect(text).toContain('Explore: message classification')
  })

  it('does not format title when description does not start with subagent name', () => {
    const text = renderToolUseText('Agent', { description: 'Search codebase', subagent_type: 'Explore' })
    expect(text).toContain('Search codebase')
    expect(text).not.toContain('Explore:')
  })

  it('shows only description without stats (child data fields removed)', () => {
    const text = renderToolUseText('Agent', { description: 'Search' })
    expect(text).toBe('Search')
    expect(text).not.toContain('tokens')
    expect(text).not.toContain('tool uses')
  })
})

/** Build a fake AgentChatMessage with JSON content (uncompressed). */
function makeFakeMessage(content: Record<string, unknown>): AgentChatMessage {
  return {
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
  } as unknown as AgentChatMessage
}

// A pending write states the change it ASKS for, under the shared "Requested
// changes" heading every provider draws for one. That claims nothing about the
// file: the diff itself replaces it the moment the result lands, which is the
// case below that pairs the two rows.
describe('write/edit tool_use messages state the change they request', () => {
  const writeInput = { file_path: '/tmp/test.go', content: 'package main\n\nfunc main() {}\n' }

  it('states a Write as a request while no result has landed', () => {
    const category = { kind: 'tool_use' } as MessageCategory
    const { container } = render(() =>
      renderMessageContent(makeToolUseMessage('Write', writeInput), undefined, category, AgentProvider.CLAUDE_CODE),
    )
    expect(container.textContent).toContain('Requested changes')
    expect(container.textContent).toContain('package main')
    // The header still surfaces the file path.
    expect(container.textContent).toContain('test.go')
  })

  it('drops the request once the paired tool_result lands', () => {
    const toolResultParsed = resolveMessageForRendering(parseMessageContent(makeFakeMessage({
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'test-id', content: 'Updated successfully.' }] },
      tool_use_result: {
        type: 'update',
        filePath: '/tmp/test.go',
        structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-old', '+new'] }],
      },
    })), AgentProvider.CLAUDE_CODE)
    const context: MessageContentRenderContext = { sources: testMessageSources({ result: () => (toolResultParsed) }) }
    const category = { kind: 'tool_use' } as MessageCategory
    const { container } = render(() =>
      renderMessageContent(makeToolUseMessage('Write', writeInput), context, category, AgentProvider.CLAUDE_CODE),
    )
    // The result row draws the diff that landed, so the request must not repeat
    // the content it asked for.
    expect(container.textContent).not.toContain('Requested changes')
    expect(container.textContent).not.toContain('package main')
  })

  it('states an Edit as the substitution it requests', () => {
    const editInput = {
      file_path: '/tmp/test.go',
      old_string: 'beforeMarkerXYZ',
      new_string: 'afterMarkerXYZ',
    }
    const category = { kind: 'tool_use' } as MessageCategory
    const { container } = render(() =>
      renderMessageContent(makeToolUseMessage('Edit', editInput), undefined, category, AgentProvider.CLAUDE_CODE),
    )
    expect(container.textContent).toContain('Requested changes')
    expect(container.textContent).toContain('beforeMarkerXYZ')
    expect(container.textContent).toContain('afterMarkerXYZ')
    // Header still shows the file path.
    expect(container.textContent).toContain('test.go')
  })
})

/** Build a Read tool_result message without structured tool_use_result. */
function makeReadToolResult(resultContent: string, context?: Partial<MessageContentRenderContext>) {
  const parsed = {
    type: 'user',
    message: {
      role: 'user',
      content: [{
        tool_use_id: 'test-read',
        type: 'tool_result',
        content: resultContent,
      }],
    },
  }
  return {
    parsed,
    render: () => {
      const category: MessageCategory = { kind: 'tool_result' }
      const result = renderMessageContent(parsed, { spanType: 'Read', ...context }, category, AgentProvider.CLAUDE_CODE)
      const { container } = render(() => result)
      return container
    },
  }
}

describe('read tool_result without structured data renders as ReadResultView', () => {
  it('renders tab-delimited content with line numbers', () => {
    const container = makeReadToolResult('1\tfoo\n2\tbar\n3\tbaz').render()
    // ReadFileResultView renders line numbers as distinct elements.
    expect(container.textContent).toContain('1')
    expect(container.textContent).toContain('foo')
    expect(container.textContent).toContain('bar')
    expect(container.textContent).toContain('baz')
    // Should use codeViewContainer (ReadResultView), not toolResultContentPre.
    expect(container.querySelector('[class*="codeView"]')).not.toBeNull()
  })

  it('strips [result-id: ...] suffix and still renders as ReadResultView', () => {
    const container = makeReadToolResult('1\tfoo\n2\tbar\n\n[result-id: r7]').render()
    expect(container.querySelector('[class*="codeView"]')).not.toBeNull()
    expect(container.textContent).toContain('foo')
    expect(container.textContent).not.toContain('result-id')
  })

  it('falls back to preformatted text for non-parseable content', () => {
    const container = makeReadToolResult('this is not cat-n output').render()
    // Should render as ToolResultMessage (pre text), not ReadResultView.
    expect(container.querySelector('[class*="codeView"]')).toBeNull()
    expect(container.textContent).toContain('this is not cat-n output')
  })
})

describe('renderMessageContent provider resolution', () => {
  it('renders raw JSON without guessing Claude for an unregistered provider', () => {
    // No `?? CLAUDE_CODE` fallback: an unregistered provider has no plugin, so
    // renderMessageContent must drop to the raw-JSON span rather than rendering
    // another provider's bytes through Claude's renderers.
    const parsed = { type: 'result', subtype: 'success', duration_ms: 1095 }
    const result = renderMessageContent(parsed, undefined, { kind: 'result_divider' }, 999 as AgentProvider)
    const { container } = render(() => result)
    // The Claude renderer would have produced "Turn ended (1.1s)"; instead the row
    // reaches the last-resort card, which claims nothing about what it holds.
    expect(container.textContent).toContain('LeapMux has no display for this row')
    expect(container.textContent).not.toContain('Turn ended')
    expect(container.textContent).not.toContain('Turn ended (1.1s)')
  })

  it('renders the durable interruption marker after retained provider output', () => {
    const parsed = {
      type: 'assembled_message',
      kind: 'text',
      text: 'partial output',
      completion: 'interrupted',
    }
    const result = renderMessageContent(parsed, undefined, { kind: 'assistant_text' })
    const { container } = render(() => result)
    expect(container.textContent).toContain('partial output')
    expect(container.textContent).toContain('Text truncated by interruption.')
  })

  it('uses typed completion metadata when provider content has no metadata object', () => {
    const parsed = { type: 'tool', output: 'partial' }
    const category = { kind: 'unknown' } as MessageCategory
    const result = renderMessageContent(parsed, undefined, category, AgentProvider.CODEX, MessageCompletion.ERROR)
    const { container } = render(() => result)
    expect(container.textContent).toContain('Text truncated by an error.')
  })
})

// The last-resort path used to print the whole frame as a paragraph of text. A live
// census found one on GitHub Copilot: `session.task_complete` reached the transcript as
// `{"jsonrpc":"2.0","method":"session.event",...}`, which is the raw-JSON row the shared
// standard forbids.
describe('the row no renderer claimed', () => {
  const frame = { jsonrpc: '2.0', method: 'session.event', params: { event: { type: 'not.a.known.event' } } }

  it('states that LeapMux has no display for it, and does not print the frame', () => {
    const { container } = render(() => renderMessageContent(frame, undefined, { kind: 'unknown' } as MessageCategory, AgentProvider.GITHUB_COPILOT))
    expect(container.textContent).toContain('LeapMux has no display for this row')
    expect(container.textContent).not.toContain('jsonrpc')
  })

  // The frame is the only content the row has, so it is one click away rather than gone.
  it('keeps the frame in the body the expand control opens', () => {
    const context: MessageContentRenderContext = { getMessageUiState: () => true, setMessageUiState: () => {} }
    const { container } = render(() => renderMessageContent(frame, context, { kind: 'unknown' } as MessageCategory, AgentProvider.GITHUB_COPILOT))
    expect(container.textContent).toContain('not.a.known.event')
  })

  it('limits a large frame after the expand control opens it', () => {
    const context: MessageContentRenderContext = { getMessageUiState: () => true, setMessageUiState: () => {} }
    const source = `UNKNOWN_HEAD${'x'.repeat(100_000)}UNKNOWN_TAIL`
    const { container } = render(() => renderMessageContent(source, context, { kind: 'unknown' } as MessageCategory))

    expect(container.textContent!.length).toBeLessThan(source.length)
    expect(container.textContent).toContain('UNKNOWN_HEAD')
    expect(container.textContent).toContain('UNKNOWN_TAIL')
    expect(container.textContent).toContain('Display limited')
  })

  // A row that THREW is a different statement from one nobody claimed, and the card
  // separates them. It stays neutral about the cause, because both a defect in LeapMux
  // and content no parser accepts land here.
  it('separates a row that could not be rendered from one nobody claimed', () => {
    const { container } = render(() => renderMessageContent('{ this is not json', undefined, { kind: 'unknown' } as MessageCategory))
    expect(container.textContent).toContain('LeapMux could not render this row')
    expect(container.textContent).not.toContain('has no display')
  })
})

describe('thinking renderer honors context.expandUiKey', () => {
  it('reads expand-state under the context-supplied key, not its own THINKING literal', () => {
    const parsed = { type: 'assistant', message: { content: [{ type: 'thinking', thinking: 'a long private thought' }] } }
    const category = { kind: 'assistant_thinking' } as MessageCategory
    const getMessageUiState = vi.fn().mockReturnValue(false)
    const setMessageUiState = vi.fn()
    // The classification mapper resolved this row's expand key; the renderer must
    // look its shared UI-state up under THAT key, not the hand-typed THINKING literal
    // it falls back to only without a context. PLAN_EXECUTION stands in for "a key
    // that is not the fallback" -- what the case proves is the lookup, not the key.
    const context: MessageContentRenderContext = {
      expandUiKey: MESSAGE_UI_KEY.PLAN_EXECUTION,
      getMessageUiState,
      setMessageUiState,
    }
    render(() => renderMessageContent(parsed, context, category, AgentProvider.CLAUDE_CODE))
    expect(getMessageUiState).toHaveBeenCalledWith(MESSAGE_UI_KEY.PLAN_EXECUTION)
    expect(getMessageUiState).not.toHaveBeenCalledWith(MESSAGE_UI_KEY.THINKING)
  })

  it('reads an assembled reasoning row under the context-supplied key too', () => {
    // Codex, ZCode, Pi and every Agent Client Protocol provider store reasoning as
    // one assembled row, which renderMessageContent draws before any plugin runs.
    // ChatView premeasures that row under expandedUiKeyFor(kind), so the bubble must
    // read the same key. Under a different key the stored expansion and the measured
    // height disagree as soon as the reader collapses the row.
    const parsed = assembledMessageRow('reasoning', 'a long private thought')
    const getMessageUiState = vi.fn().mockReturnValue(false)
    const setMessageUiState = vi.fn()
    const context: MessageContentRenderContext = {
      expandUiKey: MESSAGE_UI_KEY.PLAN_EXECUTION,
      getMessageUiState,
      setMessageUiState,
    }
    render(() => renderMessageContent(parsed, context, { kind: 'assistant_thinking' }, AgentProvider.CODEX))
    expect(getMessageUiState).toHaveBeenCalledWith(MESSAGE_UI_KEY.PLAN_EXECUTION)
    expect(getMessageUiState).not.toHaveBeenCalledWith(MESSAGE_UI_KEY.THINKING)
  })
})

describe('a user row whose provider has no plugin', () => {
  // LeapMux writes every user row itself, in its own flat `{content, attachments?}`
  // shape, so it needs no plugin to draw. An agent tab projected from the CRDT carries no
  // agentProvider until useTabHydrators fetches the worker-side metadata. Without
  // the neutral branch this fell through to the raw-JSON span.
  it('draws the same card a registered provider would, not raw JSON', () => {
    const parsed = { content: 'hello there' }
    const category = { kind: 'user_content' } as MessageCategory

    const neutral = render(() => renderMessageContent(parsed, undefined, category, AgentProvider.UNSPECIFIED))
    expect(neutral.container.textContent).toContain('hello there')
    expect(neutral.container.textContent).not.toContain('"content"')

    // Keep the markup identical to the plugin path to prevent a hydration-time resize.
    const viaPlugin = render(() => renderMessageContent(parsed, undefined, category, AgentProvider.CLAUDE_CODE))
    expect(neutral.container.innerHTML).toBe(viaPlugin.container.innerHTML)
  })

  it('lists the attachments carried on the same payload', () => {
    const parsed = { content: 'see this', attachments: [{ filename: 'diagram.png', mime_type: 'image/png' }] }
    const { container } = render(() =>
      renderMessageContent(parsed, undefined, { kind: 'user_content' } as MessageCategory, AgentProvider.UNSPECIFIED))
    expect(container.textContent).toContain('diagram.png')
    // Not merely present in a serialized blob: the raw-JSON span this replaced
    // also "contained" the filename, so assert the payload's own keys are gone.
    expect(container.textContent).not.toContain('mime_type')
    expect(container.textContent).toContain('see this')
  })

  it('draws an attachment-only send, whose content is the empty string', () => {
    // A file dragged in with no typed text. The empty string is the boundary the
    // neutral branch has to pass through to UserContentMessage: a renderer that
    // treated it as "nothing to draw" would show an empty card for a real message.
    const parsed = { content: '', attachments: [{ filename: 'notes.pdf', mime_type: 'application/pdf' }] }
    const { container } = render(() =>
      renderMessageContent(parsed, undefined, { kind: 'user_content' } as MessageCategory, AgentProvider.UNSPECIFIED))
    expect(container.textContent).toContain('notes.pdf')
    expect(container.textContent).not.toContain('mime_type')
  })

  it('still drops an AGENT-shaped row to the last-resort card', () => {
    // The neutral branch is keyed on the category, and only a USER row reaches
    // `user_content` without a plugin (see classifyMessage). A provider's own
    // unreadable bytes must still reach the last-resort card rather than a user card.
    const parsed = { type: 'result', subtype: 'success', duration_ms: 1095 }
    const { container } = render(() =>
      renderMessageContent(parsed, undefined, { kind: 'unsupported_provider' } as MessageCategory, AgentProvider.UNSPECIFIED))
    expect(container.textContent).toContain('LeapMux has no display for this row')
  })
})

describe('provider completion fields', () => {
  it('does not interpret a provider field as LeapMux completion', () => {
    const message = { ...makeToolUseMessage('Read', { file_path: '/a.ts' }), _leapmux: { completion: 'error' } }
    const { container } = render(() => renderMessageContent(message, undefined, { kind: 'tool_use' } as MessageCategory, AgentProvider.CLAUDE_CODE))
    expect(container.querySelector('[role="note"]')).toBeNull()
    expect(container.textContent).toContain('/a.ts')
  })
})
