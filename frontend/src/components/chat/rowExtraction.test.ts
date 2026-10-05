import type { ChatRow } from './model/row'
import type { ToolCall } from './model/toolCall'
import type { ProviderTranscriptCapability } from './providers/capabilities'
import type { ChatRowExtraction } from './rowExtraction'
import type { ResolvedMessageContent, RowExtractionInput } from './rowExtractionTypes'
import { describe, expect, it, vi } from 'vitest'
import { NOTIFICATION_TYPE } from '~/generated/contracts/worker-vocab'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { toolCallFixture, toolRow } from '~/test-support/toolCallFixture'
import { pluginFor, resolveMessageForRendering } from './providers/registry'
import { extractChatRow, extractedRow } from './rowExtraction'
// Side-effect import: the three branches below dispatch through the registry.
import './providers'

/*
 * The three CROSS-PROVIDER branches of the row extractor.
 *
 * A turn end, a notification thread and a control response are not any one
 * provider's shape: every provider ends a turn, the worker threads notifications
 * from all of them, and LeapMux writes the control response itself. Each used to take
 * a path AROUND the row model -- a hook that predated it, or a branch in MessageBubble --
 * which left the matching `ChatRow` variant declared and never produced, and the
 * render case behind it unreachable.
 */
function parsed(parent: Record<string, unknown>, provider: AgentProvider = AgentProvider.CLAUDE_CODE): ResolvedMessageContent {
  return resolveMessageForRendering({ wrapper: null, topLevel: parent, parentObject: parent, rawText: '', supplementalContent: undefined, messageMetadata: undefined }, provider)
}

/** The row an extraction produced, or null for either rowless outcome. */
function rowOf(extraction: ChatRowExtraction): ChatRow | null {
  return extractedRow(extraction)
}

/**
 * Force a property read to throw so the control-response guard cannot read the original frame.
 * Ordinary malformed values use the readers' safe defaults and cannot prove that guard.
 */
function exploding(): Record<string, unknown> {
  return {
    get type(): string {
      throw new Error('unreadable')
    },
  }
}

/** Exercise the shared entrypoint with a controlled neutral row and path capability. */
function withOutputPaths(
  paths: ProviderTranscriptCapability['outputFilePaths'],
  run: (call: ToolCall, input: () => RowExtractionInput | undefined) => void,
  result?: ChatRow | null,
) {
  const plugin = pluginFor(AgentProvider.CLAUDE_CODE)
  if (!plugin)
    throw new Error('The registered fixture provider is absent.')
  const previous = Object.getOwnPropertyDescriptor(plugin.transcript, 'outputFilePaths')
  const call = toolCallFixture('execute', { result: { commands: [{ output: 'The native preview.' }], unresolvedTerminals: [] } })
  const extracted = vi.spyOn(plugin.transcript, 'extractRow').mockReturnValue(result === undefined ? toolRow(call) : result)
  Object.defineProperty(plugin.transcript, 'outputFilePaths', { configurable: true, writable: true, value: paths })
  try {
    run(call, () => extracted.mock.calls[0]?.[0])
  }
  finally {
    extracted.mockRestore()
    if (previous)
      Object.defineProperty(plugin.transcript, 'outputFilePaths', previous)
    else
      Reflect.deleteProperty(plugin.transcript, 'outputFilePaths')
  }
}

describe('extractChatRow output paths', () => {
  it('keeps the call unchanged when the provider supplies no path capability', () => {
    withOutputPaths(undefined, (call) => {
      const row = rowOf(extractChatRow(AgentProvider.CLAUDE_CODE, parsed({ content: 'neutral fixture' }), { kind: 'tool_result' }))
      expect(row?.kind).toBe('tool')
      if (row?.kind !== 'tool')
        throw new Error('The controlled tool row is absent.')
      expect(row.call).toBe(call)
    })
  })

  it('passes the same extraction input and selected call to the provider and preserves preview data', () => {
    const paths = ['/native/first.log', ' /native/with spaces.log ', '/native/first.log']
    const hook = vi.fn(() => paths)
    withOutputPaths(hook, (call, input) => {
      const resolved = parsed({ content: 'neutral fixture' })
      const row = rowOf(extractChatRow(AgentProvider.CLAUDE_CODE, resolved, { kind: 'tool_result' }, { spanId: 'selected-span' }))
      expect(hook).toHaveBeenCalledWith(input(), call)
      expect(hook).toHaveBeenCalledOnce()
      if (row?.kind !== 'tool')
        throw new Error('The path capability did not retain its tool row.')
      expect(row.call.outputFilePaths).toEqual(paths.slice(0, 2))
      expect(row.call.result).toBe(call.result)
      expect(row.call.request).toBe(call.request)
      paths[0] = '/changed-after-extraction'
      expect(row.call.outputFilePaths?.[0]).toBe('/native/first.log')
    })
  })

  it('keeps the original call when the path capability returns an empty list', () => {
    withOutputPaths(() => [], (call) => {
      const row = rowOf(extractChatRow(AgentProvider.CLAUDE_CODE, parsed({ content: 'neutral fixture' }), { kind: 'tool_result' }))
      if (row?.kind !== 'tool')
        throw new Error('The controlled tool row is absent.')
      expect(row.call).toBe(call)
    })
  })

  it('does not call the path capability for a non-tool row or an unsupported frame', () => {
    const hook = vi.fn(() => ['/native/ignored.log'])
    for (const result of [{ kind: 'assistant-text', text: 'The native answer.' } as const, null]) {
      withOutputPaths(hook, () => {
        extractChatRow(AgentProvider.CLAUDE_CODE, parsed({ content: 'neutral fixture' }), { kind: 'assistant_text' })
      }, result)
    }
    expect(hook).not.toHaveBeenCalled()
  })

  it('retains the original payload when the path capability fails', () => {
    const failure = new Error('The controlled path extractor fails.')
    withOutputPaths(() => {
      throw failure
    }, () => {
      const frame = { content: 'neutral fixture' }
      const extraction = extractChatRow(AgentProvider.CLAUDE_CODE, parsed(frame), { kind: 'tool_result' })
      expect(extraction.kind).toBe('failed')
      if (extraction.kind !== 'failed')
        throw new Error('The path failure did not produce an extraction failure.')
      expect(extraction.payload).toBe(frame)
      expect(extraction.error).toBe(failure)
    })
  })

  it('refuses an invalid neutral path without reading its contents', () => {
    withOutputPaths(() => ['/native/\0invalid'], () => {
      expect(extractChatRow(AgentProvider.CLAUDE_CODE, parsed({ content: 'neutral fixture' }), { kind: 'tool_result' }).kind).toBe('failed')
    })
  })
})

describe('extractChatRow turn end', () => {
  const claudeResult = { type: 'result', subtype: 'success', duration_ms: 1200, num_tool_uses: 3, total_cost_usd: 0.25 }

  it('reads the provider label and the totals the worker measured', () => {
    const row = rowOf(extractChatRow(AgentProvider.CLAUDE_CODE, parsed(claudeResult), { kind: 'result_divider' }))
    expect(row?.kind).toBe('divider')
    if (row?.kind !== 'divider')
      return
    expect(row.divider.label).toContain('Turn ended')
    expect(row.divider.meta).toEqual({ durationMs: 1200, costUsd: 0.25, numToolUses: 3 })
  })

  // The plugin reads its OWN envelope, so a frame it does not recognize yields no row
  // and the reader gets the unrecognized card rather than an empty rule. The outcome
  // carries the frame, because that card has nothing else to show.
  it('answers unsupported for a frame the provider does not recognize', () => {
    const frame = { type: 'not-a-result' }
    expect(extractChatRow(AgentProvider.CLAUDE_CODE, parsed(frame), { kind: 'result_divider' }))
      .toEqual({ kind: 'unsupported', payload: frame, completion: null })
  })

  it('answers unsupported for a provider with no plugin', () => {
    expect(extractChatRow(undefined, parsed(claudeResult), { kind: 'result_divider' }))
      .toEqual({ kind: 'unsupported', payload: claudeResult, completion: null })
  })
})

describe('extractChatRow notification thread', () => {
  const cleared = { type: NOTIFICATION_TYPE.ContextCleared }

  it('reads every message of the thread into one row', () => {
    const row = rowOf(extractChatRow(AgentProvider.CLAUDE_CODE, parsed(cleared), {
      kind: 'notification',
      entries: [{ kind: 'context-cleared' }, { kind: 'context-cleared' }],
    }))
    expect(row?.kind).toBe('notification')
    if (row?.kind !== 'notification')
      return
    expect(row.thread.entries).toHaveLength(2)
  })

  // A thread that states NOTHING must yield no row, so the frame falls to the
  // unrecognized card. Returning an EMPTY thread instead would draw a bubble with
  // nothing in it, which is what the legacy path avoided by falling back to raw JSON.
  it('answers unsupported for a category with no entries', () => {
    expect(extractChatRow(AgentProvider.CLAUDE_CODE, parsed(cleared), { kind: 'notification', entries: [] }))
      .toEqual({ kind: 'unsupported', payload: cleared, completion: null })
  })
})

describe('extractChatRow control response', () => {
  const response = { requestId: 'r1', claimToken: 'c1', request: undefined, response: { response: { response: { behavior: 'allow' } } } }

  // LeapMux writes this row itself, so it needs no plugin -- and it must reach the model
  // rather than skipping it, or the transcript needs a second render path for it.
  //
  // The row carries the DISPLAY, not the native payloads: layer 1 runs the provider's
  // own derivation once, so the transcript row and the scroll-rail dot cannot state
  // two different answers for one row.
  it('carries the derived display, with no plugin involved', () => {
    expect(rowOf(extractChatRow(undefined, parsed({}), { kind: 'control_response', response })))
      .toEqual({ kind: 'control-response', display: { kind: 'label', text: 'Allow' } })
  })

  // Degradation is the chokepoint's job, and the extraction must not bypass it: a
  // response no derivation recognizes still draws the neutral label rather than
  // falling to the card that says LeapMux has no display for the row.
  it('degrades to the neutral label for a response no derivation reads', () => {
    const unreadable = { requestId: 'r2', claimToken: 'c2', request: undefined, response: {} }
    expect(rowOf(extractChatRow(AgentProvider.CODEX, parsed({}, AgentProvider.CODEX), { kind: 'control_response', response: unreadable })))
      .toEqual({ kind: 'control-response', display: { kind: 'label', text: 'Responded' } })
  })

  // The row states LeapMux's own record of the answer, and the ORIGINAL frame beside
  // it may be one that never parsed. The answer does not depend on that frame, so an
  // unreadable one must not cost the reader the row -- `renderMessageContent` reads
  // this category BEFORE it parses anything for the same reason.
  it('states the answer even when the original frame is unreadable', () => {
    expect(rowOf(extractChatRow(AgentProvider.CODEX, parsed(exploding(), AgentProvider.CODEX), { kind: 'control_response', response })))
      .toEqual({ kind: 'control-response', display: { kind: 'label', text: 'Allow' } })
  })
})

/*
 * A user row is LeapMux's own too: it persists `{content, attachments?}`, which
 * carries no provider frame at all.
 */
describe('extractChatRow user content', () => {
  // The regression this branch exists to close. A tab can lack worker metadata while
  // hydration runs, so its provider is UNSPECIFIED and `pluginFor` answers nothing --
  // but `classifyMessage` still calls the row `user_content` and the transcript still
  // draws it. Without the shared branch the scroll rail's dot lost its preview while
  // the row beside it showed the text.
  it('reads the row for a provider that has no plugin', () => {
    expect(rowOf(extractChatRow(undefined, parsed({ content: 'do the thing' }), { kind: 'user_content' })))
      .toEqual({ kind: 'user', text: 'do the thing', attachments: [] })
  })

  it('reads the row the same way for a registered provider', () => {
    expect(rowOf(extractChatRow(AgentProvider.CLAUDE_CODE, parsed({ content: 'do the thing' }), { kind: 'user_content' })))
      .toEqual({ kind: 'user', text: 'do the thing', attachments: [] })
  })

  it('carries the attachments the row names', () => {
    const payload = { content: '', attachments: [{ filename: 'shot.png', mime_type: 'image/png' }] }
    expect(rowOf(extractChatRow(undefined, parsed(payload), { kind: 'user_content' })))
      .toEqual({ kind: 'user', text: '', attachments: [{ filename: 'shot.png', mimeType: 'image/png' }] })
  })

  // A row LeapMux wrote with nothing in it is HIDDEN, which states something
  // different from "nobody could read this frame".
  it('hides a row that carries neither text nor an attachment', () => {
    expect(rowOf(extractChatRow(undefined, parsed({ content: '  ' }), { kind: 'user_content' })))
      .toEqual({ kind: 'hidden' })
  })

  // A provider can classify its own native frame as `user_content`. Only the
  // provider reads that frame, so the shared LeapMux reader must not answer for it.
  it('lets a registered provider read its own user row', () => {
    const plugin = pluginFor(AgentProvider.CLAUDE_CODE)
    if (!plugin)
      throw new Error('The registered fixture provider is absent.')
    const native: ChatRow = { kind: 'user', text: 'the native prompt', attachments: [] }
    const extract = vi.spyOn(plugin.transcript, 'extractRow').mockReturnValue(native)
    try {
      const resolved = parsed({ content: 'the LeapMux text' })
      expect(rowOf(extractChatRow(AgentProvider.CLAUDE_CODE, resolved, { kind: 'user_content' }))).toBe(native)
      expect(extract).toHaveBeenCalledOnce()
    }
    finally {
      extract.mockRestore()
    }
  })

  it('reports a registered provider that reads no row for its user frame as unsupported', () => {
    const plugin = pluginFor(AgentProvider.CLAUDE_CODE)
    if (!plugin)
      throw new Error('The registered fixture provider is absent.')
    const extract = vi.spyOn(plugin.transcript, 'extractRow').mockReturnValue(null)
    try {
      const frame = { content: 'the LeapMux text' }
      expect(extractChatRow(AgentProvider.CLAUDE_CODE, parsed(frame), { kind: 'user_content' }))
        .toMatchObject({ kind: 'unsupported', payload: frame })
    }
    finally {
      extract.mockRestore()
    }
  })
})

/*
 * LeapMux's own assembled envelope: the worker joins a run of streamed chunks into one
 * message, and no provider ever sends that shape. The transcript, the scroll rail and
 * Copy-Markdown each used to parse it themselves, and the completion marker landed
 * inside the text on one path and in a note beside it on another.
 */
describe('extractChatRow assembled message', () => {
  const envelope = (kind: string, completion = 'complete') => ({
    type: 'assembled_message',
    kind,
    text: 'partial output',
    completion,
  })

  it.each([
    ['assistant_text', 'text', 'assistant-text'],
    ['assistant_thinking', 'reasoning', 'assistant-thinking'],
    ['assistant_plan', 'plan', 'assistant-plan'],
  ] as const)('reads a %s envelope into its own prose row', (category, kind, expected) => {
    expect(rowOf(extractChatRow(AgentProvider.CLAUDE_CODE, parsed(envelope(kind)), { kind: category })))
      .toEqual({ kind: expected, text: 'partial output' })
  })

  // No plugin is consulted, so a tab whose worker metadata has not loaded still draws
  // every assembled row it holds.
  it('reads the envelope for a provider with no plugin', () => {
    expect(rowOf(extractChatRow(undefined, parsed(envelope('text')), { kind: 'assistant_text' })))
      .toEqual({ kind: 'assistant-text', text: 'partial output' })
  })

  // The CATEGORY picks the row kind, because the classifier reads the worker's
  // `assembled_kind` column before the envelope's own field. A reader that took the
  // field alone answered a different row than the virtual list had measured.
  it('takes the row kind from the category when the envelope disagrees', () => {
    expect(rowOf(extractChatRow(AgentProvider.CLAUDE_CODE, parsed(envelope('text')), { kind: 'assistant_thinking' })))
      .toEqual({ kind: 'assistant-thinking', text: 'partial output' })
  })

  // The completion rides on the OUTCOME rather than inside the text, so each reader
  // draws the notice its own surface wants -- a note beside the transcript row, a
  // suffix under the rail's dot -- from one answer.
  it('reports the completion the envelope states, leaving the text alone', () => {
    const extraction = extractChatRow(AgentProvider.CLAUDE_CODE, parsed(envelope('text', 'interrupted')), { kind: 'assistant_text' })
    expect(extraction.completion).toBe('interrupted')
    expect(rowOf(extraction)).toEqual({ kind: 'assistant-text', text: 'partial output' })
  })

  // LeapMux's own column wins, because a retained frame can state a completion the
  // worker later contradicted.
  it('prefers the completion LeapMux recorded over the envelope', () => {
    const extraction = extractChatRow(
      AgentProvider.CLAUDE_CODE,
      parsed(envelope('text', 'interrupted')),
      { kind: 'assistant_text' },
      { completion: MessageCompletion.ERROR },
    )
    expect(extraction.completion).toBe('error')
  })
})

describe('extractChatRow hidden', () => {
  // A hidden row draws nothing, whatever its provider, and it says so as a ROW. The
  // rule used to live in the transcript renderer, so the scroll rail and the image
  // tab relied on every plugin's switch happening to fall through for the category.
  it('answers a hidden row rather than no row', () => {
    expect(extractChatRow(AgentProvider.CLAUDE_CODE, parsed({ type: 'anything' }), { kind: 'hidden' }))
      .toEqual({ kind: 'row', row: { kind: 'hidden' }, completion: null })
  })
})

describe('extractChatRow failure', () => {
  // Three of the four readers run outside the render tree, where a throw reaches an
  // effect with no way to draw the failure -- so a malformed frame degrades instead.
  // It degrades to `failed`, NOT to `unsupported`: the two draw different cards, and
  // only one of them blames LeapMux for a defect that is LeapMux's.
  it('reports a failed extraction rather than throwing', () => {
    const frame = { type: 'result' }
    const resolved = parsed(frame)
    const plugin = pluginFor(AgentProvider.CLAUDE_CODE)
    if (!plugin?.transcript.extractDivider)
      throw new Error('The registered Claude plugin has no divider extractor.')
    const failure = new Error('The controlled divider extractor fails.')
    const extractor = vi.spyOn(plugin.transcript, 'extractDivider').mockImplementation(() => {
      throw failure
    })
    try {
      const extraction = extractChatRow(AgentProvider.CLAUDE_CODE, resolved, { kind: 'result_divider' })
      expect(extraction.kind).toBe('failed')
      if (extraction.kind !== 'failed')
        throw new Error('The throwing extractor did not produce a failure record.')
      expect(extraction.payload).toBe(frame)
      expect(extraction.error).toBe(failure)
      expect(extractor).toHaveBeenCalledOnce()
    }
    finally {
      extractor.mockRestore()
    }
  })

  // The distinction the two outcomes exist for, asserted as a pair so neither can
  // quietly collapse into the other.
  it('separates a failed extraction from an unsupported frame', () => {
    expect(extractChatRow(AgentProvider.CLAUDE_CODE, parsed({ type: 'not-a-result' }), { kind: 'result_divider' }).kind)
      .toBe('unsupported')
  })
})
