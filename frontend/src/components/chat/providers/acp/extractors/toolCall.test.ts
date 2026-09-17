import type { ToolCallOf, ToolResultOf } from '../../../ir/toolCall'
import type { ACPToolCallAdapter } from './toolCall'
import { describe, expect, it } from 'vitest'
import { MESSAGE_SUPPLEMENT_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider, ContentCompression, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { prettifyArgsJson } from '~/lib/jsonFormat'
import { parseMessageContent } from '~/lib/messageParser'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { toolRow } from '~/test-support/toolCallIr'
import { buildRawJsonEnvelope } from '../../../chatRawJson'
import { imagesForIR } from '../../../ir/derivations'
import { failedResult, isFailedResult, isUnparsedResult, typedResult, unparsedResult } from '../../../ir/toolCall'
import { TOOL_KINDS, type ToolKind } from '../../../ir/toolKind'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { input } from '../../testUtils'
import { classifyACPMessage } from '../classification'
import { ACP_PAYLOAD_BUILDERS, ACP_TOOL_REQUEST_OVERRIDES, acpPayloadFor, acpResultStatesNothing, acpToolCallIR, acpToolCallNeedsResult, acpToolFacts, resolveACPMessage } from './toolCall'

/**
 * `typedResult` takes `{ kind, result }` with the result key omitted when the call
 * carries none, and the lifecycle union spells that member `result?: undefined` -- a
 * present `undefined` the exact-optional rule refuses. Each test hands the call's
 * own two fields over, so the question it asks stays the one it asked.
 */
function resultArgs<K extends ToolKind>(call: ToolCallOf<K>): { kind: K, result?: ToolResultOf<K> } {
  return call.result === undefined ? { kind: call.kind } : { kind: call.kind, result: call.result }
}

describe('result wrapper resolution (ACP)', () => {
  it('resolves native result fields while retaining the original wrapper', () => {
    const raw = ' {"id":"native-result","role":"result","future":9007199254740993,"content":{"stopReason":"end_turn","usage":{"totalTokens":0}}} '
    const message = makeMessage({ agentProvider: AgentProvider.OPENCODE, content: new TextEncoder().encode(raw), contentCompression: ContentCompression.NONE })
    const parsed = parseMessageContent(message)
    const resolved = resolveACPMessage(parsed)
    expect(resolved).toEqual({ stopReason: 'end_turn', usage: { totalTokens: 0 } })
    expect(classifyACPMessage()({ ...parsed, parentObject: resolved }).kind).toBe('result_divider')
    expect(parsed.parentObject?.role).toBe('result')
    expect(parsed.rawText).toBe(raw)
    expect(buildRawJsonEnvelope(message, parsed, 'agent')).toContain(`"content":${raw}`)
  })

  it.each([
    { role: 'assistant', content: { text: 'Keep the message' } },
    { stopReason: 'end_turn' },
    { role: 'result', content: null },
    { role: 'result', content: 0 },
    { role: 'result', content: [] },
  ])('retains an unwrapped or invalid result shape (%j)', (original) => {
    expect(resolveACPMessage(input(original))).toEqual(original)
  })

  /**
   * `in` walks the prototype chain, so a protocol key the AGENT named `toString` reads
   * as "the frame already carries it" for every plain object, and the whole resolve
   * answered unchanged -- discarding a merge it had already built. The worker's map
   * lookup has no prototype chain, so the two sides did not compute the same predicate.
   */
  it.each(['toString', 'constructor', 'valueOf', 'hasOwnProperty'])('merges a protocol key named %s', (key) => {
    const frame = { sessionUpdate: 'tool_call_update', toolCallId: 'call-1', status: 'completed' }
    const resolved = resolveACPMessage({
      ...input(frame),
      supplementalContent: { ...frame, protocol: { [key]: 'from the worker' } },
    })
    expect(resolved?.[key]).toBe('from the worker')
    // The frame's OWN fields still win over the protocol payload.
    expect(resolved?.toolCallId).toBe('call-1')
  })
})

describe('an interrupted tool call (ACP)', () => {
  // The worker stores the LAST frame the agent sent, byte for byte, and keeps every
  // field an earlier frame carried in the supplement. The row therefore holds the
  // agent's own in_progress status, and the interruption lives in the completion
  // column alone.
  const original = {
    sessionUpdate: 'tool_call_update',
    toolCallId: 'tc-1',
    status: 'in_progress',
    content: [{ type: 'content', content: { type: 'text', text: 'partial output' } }],
  }
  const message = makeMessage({
    agentProvider: AgentProvider.OPENCODE,
    completion: MessageCompletion.INTERRUPTED,
    content: rawContent(original),
    supplementalContent: rawContent({
      [MESSAGE_SUPPLEMENT_FIELD.Provider]: {
        sessionUpdate: 'tool_call_update',
        toolCallId: 'tc-1',
        status: 'in_progress',
        protocol: { title: 'printf partial', kind: 'execute' },
      },
    }),
  })

  it('recovers the opening frame fields from the supplement', () => {
    expect(resolveACPMessage(parseMessageContent(message))).toEqual({
      ...original,
      title: 'printf partial',
      kind: 'execute',
    })
  })

  it('presents the recovered command and the partial output', () => {
    const parsed = parseMessageContent(message)
    const call = acpToolCallIR(
      resolveACPMessage(parsed)!,
      undefined,
      parsed.supplementalContent,
      message.completion,
    )
    expect(call.kind).toBe('execute')
    expect(call.title).toBe('printf partial')
    expect(call.kind === 'execute' ? typedResult(resultArgs(call))?.commands[0]?.output : undefined).toBe('partial output')
  })

  it('classifies the row as a tool use although its status is not final', () => {
    const parsed = parseMessageContent(message)
    expect(classifyACPMessage()({ ...parsed, completion: message.completion }).kind).toBe('tool_use')
  })
})

// `acpToolCallNeedsResult` reads the TYPED request the adapter built, so a provider
// that reclassifies a call changes what must be fetched for it.
describe('acpToolCallNeedsResult', () => {
  const execute = {
    sessionUpdate: 'tool_call',
    toolCallId: 'tc-1',
    kind: 'execute',
    status: 'pending',
    rawInput: { command: 'ls -la' },
  }

  it('asks for the result when the target is still unknown', () => {
    expect(acpToolCallNeedsResult({ ...execute, rawInput: {} }, undefined)).toBe(true)
    expect(acpToolCallNeedsResult(execute, undefined)).toBe(false)
  })

  it('reads the KIND the adapter chose, not the one the call declared', () => {
    // `agent` always asks for the result; `execute` with a command does not.
    expect(acpToolCallNeedsResult(execute, () => ({ kind: 'agent', request: { description: '', prompt: '' } }))).toBe(true)
  })

  it('reads the typed request the adapter supplied', () => {
    expect(acpToolCallNeedsResult({ ...execute, rawInput: {} }, () => ({ kind: 'execute', request: { command: 'ls -la' } }))).toBe(false)
  })
})

// A wire kind LeapMux does not know IS uncategorized, so the call gets the same
// treatment a literal `other` gets: the shared Model Context Protocol card, whose
// wrench identifies nothing the agent ran. The provider's own word survives as the label.
describe('a wire kind the shared tables do not know', () => {
  const call = (extra: Record<string, unknown> = {}) => acpToolCallIR({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'frob',
    kind: 'frobnicate',
    status: 'completed',
    title: 'Frobnicate: agent',
    rawInput: { targetModeId: 'agent' },
    ...extra,
  }, undefined, undefined)

  it('narrows the kind but keeps the provider word as the label', () => {
    expect(call().kind).toBe('mcp')
    expect(call().label).toBe('Frobnicate')
  })

  // The protocol's own `other` is not a title. A call headed "other" states less
  // than one headed "Tool", and the wire word reached the header while nothing
  // caught it.
  it('never titles a call with the bare wire word', () => {
    const untitled = acpToolCallIR({ sessionUpdate: 'tool_call', toolCallId: 'bare', kind: 'other', status: 'pending' }, undefined, undefined)
    expect(untitled.title).toBe('Tool')
    expect(untitled.kind).toBe('mcp')
  })

  it('reads a raw result object, as a literal other does', () => {
    const withRaw = call({ rawOutput: { targetModeId: 'agent' } })
    expect(withRaw.kind === 'mcp' ? typedResult(resultArgs(withRaw))?.structuredJson : undefined).toContain('targetModeId')
  })

  it('keeps the arguments on the uncategorized card, as a literal other does', () => {
    const uncategorized = call()
    expect(uncategorized.kind).toBe('mcp')
    expect(uncategorized.kind === 'mcp' ? (uncategorized.request as { args: Record<string, unknown> }).args : undefined).toEqual({ targetModeId: 'agent' })
  })
})

// Cursor's `switch_mode` is the one Agent Client Protocol kind the shared tables
// name no tool for. Its answer is the sentence the switch wrote, drawn as the prose
// the kind reads.
describe('the protocol mode switch', () => {
  const call = (status = 'completed') => acpToolCallIR({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'switch',
    kind: 'switch_mode',
    status,
    title: 'Switch Mode: agent',
    rawInput: { targetModeId: 'agent' },
    content: [{ type: 'content', content: { type: 'text', text: 'Switched to agent mode.' } }],
  }, undefined, undefined)

  it('keeps the wire kind rather than falling into the uncategorized bucket', () => {
    expect(call().kind).toBe('switch_mode')
  })

  // The kind's own label states the tool, so the provider word adds nothing.
  it('states no label of its own', () => {
    expect(call().label).toBeUndefined()
  })

  it('draws its answer as the prose the kind reads', () => {
    expect(call().kind === 'switch_mode' ? typedResult(resultArgs(call())) : undefined).toEqual({ text: 'Switched to agent mode.', format: 'plain' })
  })
})

// An adapter that set images ITSELF keeps them, because it read a provider record
// the protocol frame does not carry. Cursor's `cursor/generate_image` frame states
// the path the image was written to and holds no content block at all, so the
// shared collector would answer nothing for that row and must not erase the
// adapter's answer.
//
// The adapter states the `image` kind, exactly as Cursor's own does. A picture rides
// the call for a kind whose result is TYPED; the generic trio carries none of its own
// (invariant I6), because its pictures ride the content blocks of its result.
describe('the images one tool call carries', () => {
  const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='
  const call = { sessionUpdate: 'tool_call_update', toolCallId: 'img', status: 'completed', kind: 'other' }
  const withBlock = { ...call, content: [{ type: 'content', content: { type: 'image', data: PNG, mimeType: 'image/png' } }] }
  const generated: ACPToolCallAdapter = () => ({ kind: 'image', request: { prompt: 'a red square' }, result: {}, images: [{ filePath: '/repo/made.png' }] })

  it('keeps what the adapter supplied', () => {
    const row = acpToolCallIR(call, generated, undefined)
    expect(row.images).toEqual([{ filePath: '/repo/made.png' }])
  })

  // The card's own blocks ride its RESULT, and the shared image derivation reads
  // them there: an image tab addresses the picture without the call listing it.
  it('collects the protocol blocks into the card the shared build draws', () => {
    const row = acpToolCallIR(withBlock, undefined, undefined)
    expect(imagesForIR(toolRow(row)).map(image => image.data)).toEqual([PNG])
  })

  // An adapter that read a provider record replaces the whole payload, so the
  // bytes the same frame happens to carry never reach the card beside it.
  it('prefers the adapter over the protocol blocks', () => {
    const row = acpToolCallIR(withBlock, generated, undefined)
    expect(imagesForIR(toolRow(row))).toEqual([{ filePath: '/repo/made.png' }])
  })
})

describe('the facts-and-builder adapter (ACP)', () => {
  it.each(['pending', 'completed', 'failed', 'cancelled'])('keeps the requested change and the %s outcome apart', (status) => {
    const call = acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'edit',
      kind: 'edit',
      status,
      rawInput: { filePath: '/project/example.ts', oldString: 'before', newString: 'after' },
      content: [{ type: 'content', content: { type: 'text', text: 'No file changes occurred.' } }],
    }, undefined, undefined)
    expect(call.kind).toBe('edit')
    expect(call.kind === 'edit' && call.request.changes[0]).toMatchObject({ filePath: '/project/example.ts', oldStr: 'before' })
    if (status === 'completed')
      expect(call.result).toMatchObject({ unparsed: true, text: 'No file changes occurred.' })
  })

  it('uses the confirmed diff a completed call carries, not the requested one', () => {
    const call = acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'edit',
      kind: 'edit',
      status: 'completed',
      rawInput: { filePath: '/project/example.ts', oldString: 'before', newString: 'requested' },
      content: [{ type: 'diff', path: '/project/example.ts', oldText: 'before', newText: 'confirmed' }],
    }, undefined, undefined)
    expect(call.kind === 'edit' && call.result && 'changes' in call.result ? call.result.changes[0] : undefined).toMatchObject({ newStr: 'confirmed' })
  })

  it('folds the generic trio to the mcp kind and its card', () => {
    const call = acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'probe',
      kind: 'semantic_search',
      status: 'completed',
      rawInput: { query: 'needle' },
      content: [{ type: 'content', content: { type: 'text', text: 'found' } }],
    }, undefined, undefined)
    expect(call.kind).toBe('mcp')
    expect(call.kind === 'mcp' && call.request.tool).toBe('semantic_search')
    const typed = call.kind === 'mcp' ? typedResult(resultArgs(call)) : undefined
    expect(typed ? typed.content[0] : undefined).toMatchObject({ type: 'text', text: 'found' })
  })
})

/**
 * The lifecycle every kind takes, stated once in `acpPayloadFor`.
 *
 * Twenty kinds answered NOTHING at all before it: the shared build filled their
 * declared request and left the result slot empty, and twelve of those kinds draw no
 * request body either -- so a finished row of one drew its header over an empty card.
 */
describe('the shared ACP lifecycle', () => {
  const frame = { sessionUpdate: 'tool_call_update', toolCallId: 'tc-9', kind: 'other', title: 'report' }
  const answered = { content: [{ type: 'content', content: { type: 'text', text: 'The words it printed' } }] }

  // A `task` row reaches a reader only through a provider's own kind table, so the
  // adapter asks the shared build for it -- which is where the ladder lives.
  function row(tool: Record<string, unknown>) {
    return acpToolCallIR({ ...frame, ...tool }, facts => acpPayloadFor(facts, 'task'), undefined)
  }

  it('answers a finished arguments-only kind with the words it printed', () => {
    const call = row({ status: 'completed', ...answered })
    expect(call.kind).toBe('task')
    expect(isUnparsedResult(call.result) && call.result.text).toBe('The words it printed')
  })

  it('answers nothing while the call still runs', () => {
    expect(row({ status: 'in_progress', ...answered }).result).toBeUndefined()
  })

  it('answers the reason a failed call stated', () => {
    const call = row({ status: 'failed', ...answered })
    expect(isFailedResult(call.result) && call.result.text).toBe('The words it printed')
  })

  // A call the reader STOPPED is not a fault, so the ladder leaves its body where it
  // is. The reason branch used to replace whatever the builder produced, which cost a
  // partial read its lines, a partial search its hits and a partial edit its diff --
  // and the two rows then differ in their bodies while both head `Interrupted`.
  it('keeps the words a cancelled call printed rather than restating them as a reason', () => {
    const call = row({ status: 'cancelled', ...answered })
    expect(call.status).toBe('cancelled')
    expect(isFailedResult(call.result)).toBe(false)
    expect(isUnparsedResult(call.result) && call.result.text).toBe('The words it printed')
  })

  // The TYPED half of the same rule, and the one a reader sees: the lines that did
  // arrive stay on the row, under the kind's own body.
  it('keeps the typed body a cancelled read already collected', () => {
    const call = acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-read',
      kind: 'read',
      status: 'cancelled',
      rawInput: { filePath: '/project/a.ts' },
      content: [{ type: 'content', content: { type: 'text', text: '1\tfirst\n2\tsecond\n' } }],
    }, undefined, undefined)
    expect(call.status).toBe('cancelled')
    expect(call.kind === 'read' ? typedResult(resultArgs(call))?.lines : undefined)
      .toStrictEqual([{ num: 1, text: 'first' }, { num: 2, text: 'second' }])
  })

  // A COMPLETED call answers -- invariant I2 -- so the ladder states a result even
  // where the call printed no words. The empty unparsed brand is that statement, and
  // it costs the reader nothing: `parsedCall` strips the brand, so the card draws
  // exactly as it does for the empty result slot this case used to pin. Without it
  // the draft breaks I2 and `toolCall` degrades the whole row to the uncategorized
  // card, which drops the kind, the header and the request body the build did read.
  it('states an empty answer for a finished call that printed nothing', () => {
    const call = row({ status: 'completed' })
    expect(call.kind).toBe('task')
    expect(isUnparsedResult(call.result) && call.result.text).toBe('')
  })

  // A cancelled call that collected NOTHING states nothing under its header, exactly
  // as a completed one that printed nothing does. The `Interrupted` word still reaches
  // the reader: `toolRowStatusOutcome` composes the header from the row's own status,
  // so the empty result slot never takes it away.
  it('answers nothing for a cancelled call that printed nothing', () => {
    const call = row({ status: 'cancelled' })
    expect(call.status).toBe('cancelled')
    expect(call.result).toBeUndefined()
  })

  // The command body is BUILT to draw a failed command: it takes the call's status and
  // states the exit code beside the output. Replacing that with the reason in words is
  // what makes a row read "Error" where every other provider reads "Error (exit 1)".
  it('leaves a failed execute call its own command output', () => {
    const call = acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-10',
      kind: 'execute',
      status: 'failed',
      rawInput: { command: 'false' },
      rawOutput: { output: 'no such file', metadata: { exit: 1 } },
    }, undefined, undefined)
    expect(call.kind).toBe('execute')
    expect(call.kind === 'execute' ? typedResult(resultArgs(call))?.commands[0]?.exitCode : undefined).toBe(1)
  })

  // An EMPTY typed result is one the ladder can replace. `image` answers `{}` at every
  // state of the call, and `imageRenderer.result` draws only `revisedPrompt` -- so a
  // generation that printed words and produced no picture lost those words, whether it
  // finished, failed or was stopped. The rule lives in the ladder rather than in the
  // builder, which is what keeps the lifecycle out of all thirty builders.
  function imageRow(tool: Record<string, unknown>) {
    return acpToolCallIR({ ...frame, ...tool }, facts => acpPayloadFor(facts, 'image'), undefined)
  }

  it('answers the words a cancelled image call printed in place of its empty result', () => {
    const call = imageRow({ status: 'cancelled', ...answered })
    expect(call.status).toBe('cancelled')
    expect(isUnparsedResult(call.result) && call.result.text).toBe('The words it printed')
  })

  it('answers the words a completed image call printed in place of its empty result', () => {
    const call = imageRow({ status: 'completed', ...answered })
    expect(isUnparsedResult(call.result) && call.result.text).toBe('The words it printed')
  })

  // NOTHING replaces an empty result when the call printed nothing, because a
  // completed call must still carry one (invariant I2).
  it('keeps the empty result of a finished image call that printed nothing', () => {
    expect(imageRow({ status: 'completed' }).result).toStrictEqual({})
  })

  // A row the turn RETAINED reads as still running in its own status, and the
  // completion is the only place that says otherwise. Every reader that asked the
  // status alone dropped the file content, the hits, the page and the diff.
  it('reads the content of a retained call that never reported completion', () => {
    const call = acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-11',
      kind: 'read',
      status: 'in_progress',
      rawInput: { path: '/p/a.ts' },
      content: [{ type: 'content', content: { text: '1\tconst a = 1\n2\tconst b = 2\n' } }],
    }, undefined, undefined, MessageCompletion.COMPLETE)
    expect(call.kind).toBe('read')
    expect(call.kind === 'read' ? typedResult(resultArgs(call))?.lines?.[0]?.text : undefined).toBe('const a = 1')
  })

  // The outcome mapping runs ONE way: it completes a frame that never reported an
  // end of its own, and it never retracts the word a frame DID report. A frame
  // that states `failed` beside a COMPLETE completion is the more specific
  // statement, and overriding it worded a failed row as a clean finish.
  it.each([
    ['failed', 'failed'],
    ['cancelled', 'cancelled'],
    ['completed', 'completed'],
  ] as const)('keeps the frame own terminal status %s over the outcome mapping', (stated, expected) => {
    const call = acpToolCallIR({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'tc-terminal',
      kind: 'read',
      status: stated,
      rawInput: { path: '/p/a.ts' },
      content: [{ type: 'content', content: { text: '1\tconst a = 1\n' } }],
    }, undefined, undefined, MessageCompletion.COMPLETE)
    expect(call.status).toBe(expected)
  })
})

/**
 * The predicate the ladder's last step rests on, and the answers it must NOT give.
 *
 * It reads the VALUES and never the key count. An empty list, an empty string and a
 * `false` are each a real answer that a renderer draws an empty state for, so a rule
 * that folded them into "states nothing" would replace a to-do list somebody cleared
 * and a search that matched nothing with the words beside them.
 */
describe('acpResultStatesNothing', () => {
  it('reads an object with no stated field as one that states nothing', () => {
    expect(acpResultStatesNothing({})).toBe(true)
    // `image` is the one kind whose every field is optional, so it is the one kind a
    // builder can answer this way.
    expect(acpResultStatesNothing({ revisedPrompt: undefined })).toBe(true)
  })

  it('reads one stated field as an answer', () => {
    expect(acpResultStatesNothing({ revisedPrompt: 'a red square' })).toBe(false)
  })

  it.each([
    ['a tool that answered no content blocks', { content: [] }],
    ['a change that landed nothing', { changes: [] }],
    ['a checklist somebody cleared', { items: [] }],
    ['prose with no words in it', { text: '', format: 'plain' }],
    ['a search that found no link', { links: [], summary: '' }],
    ['a command list with no command', { commands: [], unresolvedTerminals: [] }],
    ['a listing of an empty directory', { entries: [] }],
  ])('keeps the empty answer of %s', (_case, result) => {
    expect(acpResultStatesNothing(result)).toBe(false)
  })

  // Both brands carry a `text`, so neither reads as empty and the ladder cannot replace
  // one it already stated.
  it('reads the two brands as answers', () => {
    expect(acpResultStatesNothing(unparsedResult(''))).toBe(false)
    expect(acpResultStatesNothing(failedResult(''))).toBe(false)
  })
})

// A removal and a move state their files in the ARGUMENTS and answer with a status
// alone, so the request is the only place the row can state them -- and it states them
// for the whole time the call runs, not once it finishes.
describe('the file-change requests the arguments state', () => {
  function request(kind: string, rawInput: Record<string, unknown>) {
    const call = acpToolCallIR({ sessionUpdate: 'tool_call', toolCallId: 'tc-12', kind, status: 'pending', rawInput }, undefined, undefined)
    return call.kind === 'delete' || call.kind === 'move' ? call.request.changes : []
  }

  it('names the file a delete asks to remove', () => {
    expect(request('delete', { file_path: '/p/gone.ts' })).toEqual([
      { filePath: '/p/gone.ts', operation: 'delete', oldStr: '', newStr: '', structuredPatch: null },
    ])
  })

  it.each([
    ['source_path', 'destination_path'],
    ['sourcePath', 'destinationPath'],
    ['old_path', 'new_path'],
  ])('names both files a move asks for, spelled %s and %s', (from, to) => {
    expect(request('move', { [from]: '/p/a.ts', [to]: '/p/b.ts' })).toEqual([
      { filePath: '/p/b.ts', previousPath: '/p/a.ts', operation: 'move', oldStr: '', newStr: '', structuredPatch: null },
    ])
  })

  it('names the one file a half-stated move carries', () => {
    expect(request('move', { source_path: '/p/a.ts' })).toEqual([
      { filePath: '/p/a.ts', previousPath: undefined, operation: 'move', oldStr: '', newStr: '', structuredPatch: null },
    ])
  })

  it('states no change when the arguments name no file', () => {
    expect(request('delete', {})).toEqual([])
    expect(request('move', {})).toEqual([])
  })

  // The result row exists to state the file. Once the request does, it is not needed.
  it('stops asking for the result once the request names the file', () => {
    const frame = { sessionUpdate: 'tool_call', toolCallId: 'tc-13', kind: 'delete', status: 'pending' }
    expect(acpToolCallNeedsResult({ ...frame, rawInput: {} }, undefined)).toBe(true)
    expect(acpToolCallNeedsResult({ ...frame, rawInput: { path: '/p/gone.ts' } }, undefined)).toBe(false)
  })

  // An edit that asks for several substitutions in one file states each of them. The
  // builder read one root pair alone, so a `multi_edit` opened with an empty list and
  // a header that could name no file at all.
  it('states every substitution a multi-edit asks for', () => {
    const changes = acpToolCallIR({
      sessionUpdate: 'tool_call',
      toolCallId: 'tc-14',
      kind: 'edit',
      status: 'pending',
      rawInput: { path: '/p/file.ts', edits: [{ old_string: 'firstBefore', new_string: 'firstAfter' }, { old_string: 'secondBefore', new_string: 'secondAfter' }] },
    }, undefined, undefined)
    expect(changes.kind).toBe('edit')
    expect(changes.kind === 'edit' ? changes.request.changes : []).toStrictEqual([
      { filePath: '/p/file.ts', structuredPatch: null, oldStr: 'firstBefore', newStr: 'firstAfter', showLineNumbers: false },
      { filePath: '/p/file.ts', structuredPatch: null, oldStr: 'secondBefore', newStr: 'secondAfter', showLineNumbers: false },
    ])
  })

  // A change that draws NO diff still states the file, which is the only thing the
  // header needs. Dropping it headed a failed edit with the word "Edit" and nothing.
  it('keeps the file of an edit whose arguments state no replacement text', () => {
    const call = acpToolCallIR({
      sessionUpdate: 'tool_call',
      toolCallId: 'tc-15',
      kind: 'edit',
      status: 'pending',
      rawInput: { path: '/p/file.ts', oldText: 'same', newText: 'same' },
    }, undefined, undefined)
    expect(call.kind === 'edit' ? call.request.changes.map(change => change.filePath) : []).toStrictEqual(['/p/file.ts'])
  })
})

/**
 * ONE spelling of the mode switch's arguments, shared by the builder and the table.
 *
 * `ACP_DEFAULT_REQUESTS` is total over `ToolKind`, so it held an entry for this kind
 * -- and the builder spelled its own request instead, so that entry was unreachable.
 * The two then disagreed: the entry read `mode` and `target`, the builder read `mode`
 * and `targetModeId`. No test could see the disagreement, and a reader who corrected
 * the table changed nothing. The builder now calls the table, which reads all three.
 *
 * `target` is a separate FACT from `mode`, not a second spelling of it:
 * `switchModeRenderer` draws the worktree it identifies AFTER the mode, so folding it
 * into `mode` would lose it.
 */
describe('the mode switch request', () => {
  const switchCall = (rawInput: Record<string, unknown>) => acpToolCallIR({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'switch',
    kind: 'switch_mode',
    status: 'completed',
    title: 'Switch Mode',
    rawInput,
    content: [{ type: 'content', content: { type: 'text', text: 'Switched.' } }],
  }, undefined, undefined)

  const requestOf = (rawInput: Record<string, unknown>) => {
    const call = switchCall(rawInput)
    return call.kind === 'switch_mode' ? call.request : undefined
  }

  it('reads the mode the protocol spells targetModeId', () => {
    expect(requestOf({ targetModeId: 'agent' })).toEqual({ mode: 'agent', target: undefined })
  })

  it('reads the plain mode key, and keeps it ahead of the protocol spelling', () => {
    expect(requestOf({ mode: 'plan' })?.mode).toBe('plan')
    expect(requestOf({ mode: 'plan', targetModeId: 'agent' })?.mode).toBe('plan')
  })

  // The case the dead table entry read and no row ever received. It reaches the
  // request now, so the renderer draws the worktree beside the mode.
  it('keeps the target the switch acts on', () => {
    expect(requestOf({ mode: 'worktree', target: 'feature-branch' })).toEqual({ mode: 'worktree', target: 'feature-branch' })
  })

  it('states no mode for a switch whose arguments name none', () => {
    expect(requestOf({})).toEqual({ mode: undefined, target: undefined })
  })
})

/**
 * ACP deviates from the shared request table on TWO kinds, and each reads a fact the
 * ARGUMENTS do not carry: the frame's own title, and the call's collected text. Every
 * other kind takes `DEFAULT_TOOL_REQUESTS`, which sees the arguments alone.
 *
 * Each case below states the shared answer beside the ACP one, so a later change that
 * drops an override fails here rather than quietly drawing an empty field.
 */
describe('ACP_TOOL_REQUEST_OVERRIDES', () => {
  // The rows are LIVE, because the request is what these cases ask about and it is the
  // same at every state of the call. A running row is also the state that admits no
  // result, so no case has to state an answer beside the question it asks.
  const overrideCall = (frame: Record<string, unknown>) => acpToolCallIR({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'override',
    status: 'in_progress',
    ...frame,
  }, undefined, undefined)

  const thought = [{ type: 'content', content: { type: 'text', text: 'Weighing two options.' } }]

  it('deviates on exactly the two kinds its own facts answer', () => {
    expect(Object.keys(ACP_TOOL_REQUEST_OVERRIDES).sort()).toEqual(['agent', 'think'])
  })

  it('describes a launch by the call title the frame states', () => {
    const call = overrideCall({ kind: 'agent', title: 'Inspect the parser', rawInput: { prompt: 'Read it' } })
    expect(call.kind === 'agent' ? call.request : undefined).toEqual({ description: 'Inspect the parser', prompt: 'Read it' })
    expect(DEFAULT_TOOL_REQUESTS.agent({ prompt: 'Read it' })).toEqual({ description: '', prompt: 'Read it' })
  })

  it('keeps the description the launch arguments state ahead of the frame title', () => {
    const call = overrideCall({ kind: 'agent', title: 'Task', rawInput: { description: 'Inspect the parser', instructions: 'Read it' } })
    expect(call.kind === 'agent' ? call.request : undefined).toEqual({ description: 'Inspect the parser', prompt: 'Read it' })
  })

  it('falls back to the flattened result text for a thought the arguments omit', () => {
    const call = overrideCall({ kind: 'think', rawInput: {}, content: thought })
    expect(call.kind === 'think' ? call.request : undefined).toEqual({ text: 'Weighing two options.' })
    expect(DEFAULT_TOOL_REQUESTS.think({})).toEqual({ text: '' })
  })

  it('keeps the thought the arguments state ahead of the result text', () => {
    const call = overrideCall({ kind: 'think', rawInput: { thought: 'Stated in the arguments.' }, content: thought })
    expect(call.kind === 'think' ? call.request : undefined).toEqual({ text: 'Stated in the arguments.' })
  })
})

/**
 * An argument record carrying every key `DEFAULT_TOOL_REQUESTS` reads, in each of its
 * spellings where it reads two.
 *
 * The delegated kinds below are compared over this ONE record, and the shape of it is
 * load-bearing. A probe that stated none of these keys would let a hand-written builder
 * and the shared entry agree on an EMPTY request, so the comparison would pass for a
 * kind that no longer delegates at all.
 */
const SHARED_ARGUMENT_PROBE: Record<string, unknown> = {
  channel: 'a shared channel',
  cmd: 'a shared cmd',
  command: 'a shared command',
  cron: '0 * * * *',
  description: 'a shared description',
  destinationPath: '/shared/dst.ts',
  destination_path: '/shared/dst2.ts',
  filePath: '/shared/a.ts',
  file_path: '/shared/a2.ts',
  id: 'an id',
  instructions: 'shared instructions',
  limit: 9,
  message: 'a shared message',
  mode: 'a shared mode',
  name: 'a shared name',
  newPath: '/shared/new.ts',
  new_path: '/shared/new2.ts',
  offset: 3,
  oldPath: '/shared/old.ts',
  old_path: '/shared/old2.ts',
  path: '/shared/a3.ts',
  paths: ['/shared/b.ts'],
  pattern: 'a shared pattern',
  prompt: 'a shared prompt',
  q: 'a shared q',
  query: 'a shared query',
  schedule: 'every hour',
  server: 'a shared server',
  skill: 'a shared skill',
  sourcePath: '/shared/src.ts',
  source_path: '/shared/src2.ts',
  spec: 'a shared spec',
  summary: 'a shared summary',
  target: 'a shared target',
  targetModeId: 'a shared target mode',
  taskId: 'task-2',
  task_id: 'task-1',
  text: 'a shared text',
  thought: 'a shared thought',
  to: 'a shared recipient',
  tool: 'a shared tool',
  triggerId: 'trigger-2',
  trigger_id: 'trigger-1',
  uri: 'https://shared-uri.example',
  url: 'https://shared.example',
}

/**
 * The eleven kinds whose request the ACP build reads for itself.
 *
 * TWO sources, and the list holds both: `agent` and `think` come from
 * `ACP_TOOL_REQUEST_OVERRIDES`, and the other nine spell their request inside the
 * builder -- the four that read a protocol field the shared table has no key for, the
 * two file-change kinds that read the diff out of `rawInput`, and the generic trio,
 * whose card states a server and a tool that no argument carries.
 */
const ACP_OWN_REQUEST_KINDS = ['', 'agent', 'edit', 'execute', 'fetch', 'mcp', 'other', 'read', 'search', 'think', 'write'] as const

/**
 * Every other kind, which takes the shared declared request.
 *
 * Three of them are pinned by MEMBERSHIP alone: `question` answers the constant
 * `{ questions: [] }`, `todo` answers `{ items: [] }` and `wait` answers
 * `{ durationMs: undefined }`, so a hand-written builder that answered the same
 * constant is indistinguishable from the shared one by value. What the list still
 * states is that each kind delegates at all.
 */
const ACP_SHARED_REQUEST_KINDS = [
  'agents',
  'chart',
  'delete',
  'glob',
  'grep',
  'image',
  'list',
  'memory',
  'message',
  'move',
  'question',
  'report',
  'skill',
  'switch_mode',
  'task',
  'todo',
  'trigger',
  'wait',
  'web_search',
] as const

/**
 * The builder table: one entry for each kind, checked against that kind's own request.
 *
 * Totality is the mapped type's, so a new `ToolKind` is a compile error at the table.
 * These cases pin the three statements no type makes: the keys are exactly
 * `TOOL_KINDS` at RUNTIME, each entry answers at the key that states it, and every kind
 * outside the deviation list fills the shared declared request.
 *
 * The third one is the only mechanical check that `ACP_TOOL_REQUEST_OVERRIDES` has not
 * grown past its deviations. No type can do that job: an entry that reads `args` alone
 * satisfies a slot supplying `args` and the facts, so a spread of the shared table --
 * or one stray key that shadows a kind -- compiles and simply draws a different card.
 *
 * The builders are read DIRECTLY rather than through `acpPayloadFor`, because the
 * lifecycle ladder sits above them and states the result. The request is what these
 * cases ask about, and it is the same at every state of the call.
 */
describe('ACP_PAYLOAD_BUILDERS', () => {
  const probeFacts = acpToolFacts({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'tc-probe',
    kind: 'other',
    status: 'completed',
    rawInput: SHARED_ARGUMENT_PROBE,
    content: [{ type: 'content', content: { type: 'text', text: 'The words it printed' } }],
  })

  it('states one builder for every tool kind', () => {
    expect(Object.keys(ACP_PAYLOAD_BUILDERS).sort()).toStrictEqual([...TOOL_KINDS].sort())
  })

  // Every builder runs against a frame of a DIFFERENT kind and answers its own kind. A
  // builder that reaches for a fact this frame does not carry throws here rather than
  // in the transcript, where the error boundary replaces the whole message.
  it('answers each kind at the key that states it', () => {
    for (const kind of TOOL_KINDS)
      expect(ACP_PAYLOAD_BUILDERS[kind].build(probeFacts).kind, kind || 'the empty kind').toBe(kind)
  })

  it('splits every tool kind between the two lists above', () => {
    expect([...ACP_OWN_REQUEST_KINDS, ...ACP_SHARED_REQUEST_KINDS].sort()).toStrictEqual([...TOOL_KINDS].sort())
  })

  // The probe has to REACH the facts, or the case below compares two empty requests
  // and passes for a kind that stopped delegating.
  it('carries the whole probe into the facts the builders read', () => {
    expect(probeFacts.args).toStrictEqual(SHARED_ARGUMENT_PROBE)
  })

  it.each(ACP_SHARED_REQUEST_KINDS)('fills the declared request of %s from the shared table', (kind) => {
    expect(ACP_PAYLOAD_BUILDERS[kind].build(probeFacts).request).toStrictEqual(DEFAULT_TOOL_REQUESTS[kind](probeFacts.args))
  })
})

/**
 * A tool INPUT the protocol carries as a scalar rather than as an object.
 *
 * `rawInput` is whatever the agent sent. Most daemons send an object, and the typed
 * readers need one -- but the schema permits a bare string, a number or a boolean, and
 * a normalization that answered `{}` for those threw the argument away. The row then
 * headed an uncategorized card over an empty request while the only thing the call
 * stated sat in the frame.
 *
 * The arguments therefore travel as two fields: `args` for the object a typed reader
 * indexes, and `argsText` for the JSON of anything else. A generic or fallback request
 * draws `argsText`, and a typed reader consumes `args` alone.
 */
describe('a scalar ACP tool input', () => {
  const scalar = 'a bare string argument'

  function generic(rawInput: unknown, supplemental?: unknown) {
    const call = acpToolCallIR(
      { sessionUpdate: 'tool_call_update', toolCallId: 'tc-scalar', kind: 'other', title: 'Probe', status: 'completed', rawInput },
      undefined,
      supplemental,
    )
    if (call.kind !== 'mcp')
      throw new Error(`the uncategorized kind folds to mcp, not ${call.kind || 'an unstated kind'}`)
    return call.request
  }

  it('keeps a scalar input visible as request text', () => {
    const request = generic(scalar)
    // A bare string reads as itself rather than as quoted JSON: it is the whole
    // argument, and `prettifyArgsJson` is the ONE formatter the generic card uses for
    // an object input too.
    expect(request.argsText).toBe(scalar)
    // A typed reader indexes `args`, so the scalar must not land there.
    expect(request.args).toStrictEqual({})
  })

  it.each([42, true, ['one', 'two']])('keeps a non-object input (%j) visible as request text', (rawInput) => {
    expect(generic(rawInput).argsText).toBe(prettifyArgsJson(rawInput))
  })

  // The three cases below read a LIVE row. The typed request is what they ask about,
  // and a row that has not answered is the state that admits no result -- so the case
  // states the input it is about and nothing else.
  it('still fills a typed field from an object input', () => {
    const call = acpToolCallIR(
      { sessionUpdate: 'tool_call_update', toolCallId: 'tc-read', kind: 'read', status: 'in_progress', rawInput: { filePath: '/p/a.ts' } },
      undefined,
      undefined,
    )
    expect(call.kind === 'read' && call.request.path).toBe('/p/a.ts')
  })

  // A KNOWN kind whose input is a scalar degrades to the generic card. The typed
  // request has no field the scalar can fill, so the row used to draw `read` with an
  // empty path and the argument the tool sent reached nobody at all.
  it('degrades a known kind with a scalar input to the generic card', () => {
    const call = acpToolCallIR(
      { sessionUpdate: 'tool_call_update', toolCallId: 'tc-read', kind: 'read', title: 'Read', status: 'completed', rawInput: '/p/a.ts' },
      undefined,
      undefined,
    )
    expect(call.kind).toBe('mcp')
    expect(call.kind === 'mcp' && call.request.argsText).toBe('/p/a.ts')
    expect(call.kind === 'mcp' && call.request.args).toStrictEqual({})
  })

  // An ABSENT input is NOT the scalar case, and the difference is load-bearing: a
  // file tool that states no arguments of its own recovers its path from `locations`,
  // and a degrade here would throw that recovery away.
  it('keeps a known kind whose input is absent or null', () => {
    for (const rawInput of [undefined, null]) {
      const call = acpToolCallIR(
        { sessionUpdate: 'tool_call_update', toolCallId: 'tc-read', kind: 'read', status: 'in_progress', rawInput, locations: [{ path: '/p/a.ts' }] },
        undefined,
        undefined,
      )
      expect(call.kind).toBe('read')
      expect(call.kind === 'read' && call.request.path).toBe('/p/a.ts')
    }
  })

  // The adapter is the one layer above this that may read a provider's own scalar
  // convention, so its kind must survive the degrade.
  it('lets a provider adapter state its own kind for a scalar input', () => {
    const call = acpToolCallIR(
      { sessionUpdate: 'tool_call_update', toolCallId: 'tc-exec', kind: 'execute', status: 'in_progress', rawInput: 'ls -1' },
      facts => ({ kind: 'execute', request: { command: facts.argsText ?? '' } }),
      undefined,
    )
    expect(call.kind).toBe('execute')
    expect(call.kind === 'execute' && call.request.command).toBe('ls -1')
  })

  it('keeps a scalar input the supplement put in place of an object one', () => {
    // `resolveACPMessage` replaces the whole request key from the supplement, and the
    // two halves merge only when BOTH are objects. A scalar therefore replaces the
    // object outright, and it has to survive that replacement.
    const frame = { sessionUpdate: 'tool_call_update', toolCallId: 'tc-scalar', kind: 'other', title: 'Probe', status: 'completed', rawInput: { replaced: true } }
    const resolved = resolveACPMessage({
      ...input(frame),
      supplementalContent: { sessionUpdate: 'tool_call_update', toolCallId: 'tc-scalar', status: 'completed', rawInput: scalar },
    })
    expect(resolved?.rawInput).toBe(scalar)
    const request = generic(scalar)
    expect(request.argsText).toBe(scalar)
    expect(request.args).toStrictEqual({})
  })
})

/*
 * `ThinkResult` IS `ProseResult`, so the kind declares its answer and never falls to
 * the unparsed brand. It mattered on the screen: `parsedCall` strips that brand, so
 * `thinkRenderer` read `result` as absent, drew its request line from the very same
 * string, and the row printed the first line as a summary above the whole thought.
 */
describe('the ACP think result', () => {
  const thought = 'Weighing two options.\nThe first one costs less.'
  const content = [{ type: 'content', content: { type: 'text', text: thought } }]

  function think(frame: Record<string, unknown>) {
    return acpToolCallIR({ sessionUpdate: 'tool_call_update', toolCallId: 'tc-think', kind: 'think', ...frame }, undefined, undefined)
  }

  it('answers a finished thought as prose rather than as an unparsed payload', () => {
    const call = think({ status: 'completed', rawInput: {}, content })
    expect(call.kind).toBe('think')
    expect(isUnparsedResult(call.result)).toBe(false)
    expect(call.kind === 'think' ? typedResult(resultArgs(call)) : undefined).toEqual({ text: thought, format: 'plain' })
  })

  // The thought arrives in the arguments just as often as in the content blocks, and
  // the request already folds the two. Reading the content alone answered the empty
  // string here, which suppresses the request line and draws nothing at all.
  it('answers the thought the arguments state when the call printed nothing', () => {
    const call = think({ status: 'completed', rawInput: { thought } })
    expect(call.kind === 'think' ? typedResult(resultArgs(call)) : undefined).toEqual({ text: thought, format: 'plain' })
  })

  it('answers nothing while the thought is still arriving', () => {
    expect(think({ sessionUpdate: 'tool_call', status: 'in_progress', rawInput: {}, content }).result).toBeUndefined()
  })

  it('answers the reason a failed thought stated', () => {
    const call = think({ status: 'failed', rawInput: {}, content })
    expect(isFailedResult(call.result) && call.result.text).toBe(thought)
  })

  // A thought the reader STOPPED keeps the words it wrote, under the prose body
  // the kind declares. They are the same words either way, and the brand is what the
  // renderer reads: `failedResult` draws them as the reason a call gave, which a
  // thought that was simply cut short never gave.
  it('keeps the prose a cancelled thought wrote', () => {
    const call = think({ status: 'cancelled', rawInput: {}, content })
    expect(call.status).toBe('cancelled')
    expect(isFailedResult(call.result)).toBe(false)
    expect(call.kind === 'think' ? typedResult(resultArgs(call)) : undefined).toStrictEqual({ text: thought, format: 'plain' })
  })
})
