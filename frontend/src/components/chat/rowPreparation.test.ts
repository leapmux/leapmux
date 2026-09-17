import type { ParsedMessageContent } from '~/lib/messageParser'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { MESSAGE_SUPPLEMENT_FIELD } from '~/generated/contracts/worker-vocab'
import {
  AgentChatMessageSchema,
  AgentProvider,
  ContentCompression,
  MessageCompletion,
  MessageSource,
} from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { extractedRow } from './rowExtraction'
import { extractPreparedRow, prepareChatRow, prepareMessage } from './rowPreparation'
// Side-effect import: the preparation dispatches resolution and classification through
// the registry, so every case below needs the plugins registered.
import './providers'

function message(fields: {
  provider: AgentProvider
  content: unknown
  supplement?: unknown
  completion?: MessageCompletion
  spanId?: string
  spanType?: string
}) {
  return create(AgentChatMessageSchema, {
    id: 'm1',
    source: MessageSource.AGENT,
    seq: 5n,
    agentProvider: fields.provider,
    // Omitted (not undefined) when the fixture states none; `create` rejects an explicit undefined.
    ...(fields.completion !== undefined ? { completion: fields.completion } : {}),
    spanId: fields.spanId ?? 'span-1',
    spanType: fields.spanType ?? '',
    contentCompression: ContentCompression.NONE,
    supplementalContentCompression: ContentCompression.NONE,
    content: new TextEncoder().encode(JSON.stringify(fields.content)),
    ...(fields.supplement === undefined
      ? {}
      : {
          supplementalContent: new TextEncoder().encode(
            JSON.stringify({ [MESSAGE_SUPPLEMENT_FIELD.Provider]: fields.supplement }),
          ),
          supplementalRevision: 1n,
        }),
  })
}

/** An ACP result the daemon sent inside its native envelope, and the same one bare. */
const ACP_INNER = {
  sessionUpdate: 'tool_call_update',
  toolCallId: 'call-1',
  status: 'completed',
  content: [{ type: 'content', content: { type: 'text', text: 'the output' } }],
}
const ACP_WRAPPED = { id: 'n1', role: 'result', seq: 3, content: ACP_INNER }

describe('prepareMessage', () => {
  // The defect the module exists to close. Every reader used to parse, then classify,
  // then resolve -- so the category described the RAW frame while the row was
  // extracted from the resolved one. A wrapped ACP result is the shape that proves it:
  // the native envelope carries no `sessionUpdate`, so the raw frame classifies as a
  // frame nobody can read, and only the unwrapped payload is a tool row.
  it('classifies the RESOLVED payload, not the raw frame', () => {
    const wrapped = prepareMessage(message({ provider: AgentProvider.OPENCODE, content: ACP_WRAPPED }))
    const bare = prepareMessage(message({ provider: AgentProvider.OPENCODE, content: ACP_INNER }))
    expect(wrapped.category).toEqual(bare.category)
    expect(wrapped.category.kind).toBe('tool_use')
  })

  // Both payloads are kept, because they answer different questions: the row is drawn
  // from the resolved one and the Raw JSON view shows the agent's own bytes.
  it('keeps the original parse beside the resolved one', () => {
    const prepared = prepareMessage(message({ provider: AgentProvider.OPENCODE, content: ACP_WRAPPED }))
    expect(prepared.original.parentObject).toEqual(ACP_WRAPPED)
    expect(prepared.resolved.parentObject).toEqual(ACP_INNER)
  })

  // A provider that recovers nothing resolves to the payload it was given, and the two
  // are then the SAME object -- no reader pays for a copy of bytes nobody changed.
  it('resolves to the original object when nothing was recovered', () => {
    const prepared = prepareMessage(message({ provider: AgentProvider.OPENCODE, content: ACP_INNER }))
    expect(prepared.resolved.parentObject).toBe(prepared.original.parentObject)
  })

  it('carries the message it prepared', () => {
    const source = message({ provider: AgentProvider.OPENCODE, content: ACP_INNER })
    expect(prepareMessage(source).message).toBe(source)
  })

  // ChatView holds both payloads already, and preparing a row must not parse the
  // content a second time for every mounted bubble.
  it('takes a caller supplied parse rather than parsing again', () => {
    const source = message({ provider: AgentProvider.OPENCODE, content: ACP_INNER })
    const original = parseMessageContent(source)
    const resolved: ParsedMessageContent = { ...original, parentObject: { ...ACP_INNER, status: 'pending' } }
    const prepared = prepareMessage(source, { original, resolved })
    expect(prepared.original).toBe(original)
    expect(prepared.resolved).toBe(resolved)
  })

  // The flag flips when the tab's parent link hydrates, and it re-classifies a
  // forwarded row between a collapsed prompt card and a full user message. A Claude
  // user frame carrying `parent_tool_use_id` is the one shape that reads both ways.
  it('passes the child-transcript flag to the classifier', () => {
    const forwarded = message({
      provider: AgentProvider.CLAUDE_CODE,
      content: { type: 'user', parent_tool_use_id: 'task-1', message: { role: 'user' } },
    })
    expect(prepareMessage(forwarded, { isChildTranscript: false }).category.kind).toBe('agent_prompt')
    expect(prepareMessage(forwarded, { isChildTranscript: true }).category.kind).toBe('user_text')
  })

  // A frame that never parsed still prepares: the row falls to the card that keeps
  // the bytes, and a reader that threw here would take the whole transcript with it.
  it('prepares a message whose content is not JSON', () => {
    const broken = create(AgentChatMessageSchema, {
      id: 'm3',
      source: MessageSource.AGENT,
      seq: 7n,
      agentProvider: AgentProvider.OPENCODE,
      contentCompression: ContentCompression.NONE,
      content: new TextEncoder().encode('{unfinished'),
    })
    const prepared = prepareMessage(broken)
    expect(prepared.original.topLevel).toBeNull()
    expect(prepared.category.kind).toBe('unknown')
  })

  // A tab can lack worker metadata while hydration runs, so its provider is
  // UNSPECIFIED and no plugin answers. Preparation must still reach a category.
  it('prepares a message whose provider has no plugin', () => {
    const prepared = prepareMessage(message({ provider: AgentProvider.UNSPECIFIED, content: { content: 'typed this' } }))
    expect(prepared.category.kind).toBe('unsupported_provider')
    expect(prepared.resolved.parentObject).toEqual({ content: 'typed this' })
  })
})

describe('extractPreparedRow', () => {
  // The scroll rail resolves no siblings, and it used to state NO sides at all -- so a
  // plugin that reads `sides.current` for the recovered half of a retained row read
  // nothing. The default states this message as its span's only side.
  it('states the prepared row as its own span side', () => {
    const prepared = prepareMessage(message({ provider: AgentProvider.OPENCODE, content: ACP_INNER }))
    const row = extractedRow(extractPreparedRow(prepared))
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      return
    expect(row.call.result).toBeDefined()
  })

  // The role comes from the provider's own reading of the frame, so a call that has
  // not returned is an opener and its finished row closes the span.
  it('derives the span role from the provider', () => {
    const pending = prepareMessage(message({
      provider: AgentProvider.OPENCODE,
      content: { sessionUpdate: 'tool_call', toolCallId: 'call-1', status: 'pending', title: 'Run it', kind: 'execute' },
    }))
    const finished = prepareMessage(message({ provider: AgentProvider.OPENCODE, content: ACP_INNER }))
    const roleOf = (row: ReturnType<typeof extractedRow>) => row?.kind === 'tool' ? row.role : null
    expect(roleOf(extractedRow(extractPreparedRow(pending)))).toBe('request')
    expect(roleOf(extractedRow(extractPreparedRow(finished)))).toBe('result')
  })

  // An explicit `sides` must reach the provider, or the transcript's resolved
  // siblings are lost the moment a row goes through the preparation. The opener the
  // rail cannot afford to fetch is exactly what the transcript supplies.
  it('takes an explicit sides over the derived one', () => {
    const opener = prepareMessage(message({
      provider: AgentProvider.OPENCODE,
      content: { sessionUpdate: 'tool_call', toolCallId: 'call-1', status: 'pending', title: 'Run it', kind: 'execute' },
    }))
    const prepared = prepareMessage(message({ provider: AgentProvider.OPENCODE, content: ACP_INNER }))
    const withOpener = extractedRow(extractPreparedRow(prepared, {
      sides: { current: prepared.resolved, request: opener.resolved, result: undefined, role: 'result' },
    }))
    expect(withOpener?.kind === 'tool' ? withOpener.hasRequestRow : null).toBe(true)
    // The default states no sibling at all, which is the answer for a reader that
    // resolved none -- so the two cannot be reading the same sides.
    const alone = extractedRow(extractPreparedRow(prepared))
    expect(alone?.kind === 'tool' ? alone.hasRequestRow : null).toBe(false)
  })

  // LeapMux's own completion column reaches the extraction through the message, so
  // every reader draws the same notice without passing it by hand.
  it('reports the completion the message records', () => {
    const prepared = prepareMessage(message({
      provider: AgentProvider.OPENCODE,
      content: ACP_INNER,
      completion: MessageCompletion.INTERRUPTED,
    }))
    expect(extractPreparedRow(prepared).completion).toBe('interrupted')
  })
})

describe('prepareChatRow', () => {
  it('returns the preparation and the extraction it produced', () => {
    const source = message({ provider: AgentProvider.OPENCODE, content: ACP_WRAPPED })
    const { prepared, extraction } = prepareChatRow(source)
    expect(prepared.message).toBe(source)
    expect(extraction.kind).toBe('row')
    expect(extractedRow(extraction)?.kind).toBe('tool')
  })

  // The recovered half must reach the row through the ONE preparation, which is what
  // the scroll rail and the image tab both lost by resolving after they classified.
  it('reads a recovered body into the row', () => {
    const { extraction } = prepareChatRow(message({
      provider: AgentProvider.CODEX,
      content: { threadId: 't1', turnId: 'r1', item: { type: 'commandExecution', id: 'c1', status: 'inProgress', command: 'printf partial' } },
      supplement: { itemId: 'c1', itemType: 'commandExecution', aggregatedOutput: 'Running 240 tests' },
      completion: MessageCompletion.INTERRUPTED,
    }))
    const row = extractedRow(extraction)
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool' || row.call.kind !== 'execute')
      return
    const result = row.call.result
    expect(result && 'commands' in result ? result.commands[0]?.output : null).toBe('Running 240 tests')
  })
})
