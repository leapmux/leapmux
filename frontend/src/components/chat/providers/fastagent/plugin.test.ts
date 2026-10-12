import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { failedResult } from '../../model/toolCall'
import { acpTextContent } from '../acp/testUtils'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

describe('native output without a filesystem pointer', () => {
  it('keeps a valid native result and omits the output path hook', () => {
    const frame = {
      sessionUpdate: 'tool_call_update',
      toolCallId: 'native-call',
      status: 'completed',
      kind: 'execute',
      title: 'native command',
      rawInput: {
        command: 'printf preview',
      },
      content: [
        {
          type: 'content',
          content: {
            type: 'text',
            text: 'native inline preview',
          },
        },
      ],
      rawOutput: {
        output: 'native inline preview',
        exitCode: 0,
      },
    }
    const call = providerToolCall(AgentProvider.FAST_AGENT, frame, { spanId: 'native-call', spanType: 'execute', agentSessionId: 'native-session', role: 'result' })
    expect(call).not.toBeNull()
    expect(call?.id).toBe('native-call')
    expect(call?.outputFilePaths).toBeUndefined()
    const registered = providerFor(AgentProvider.FAST_AGENT)
    expect(registered).toBeDefined()
    expect(registered?.transcript.outputFilePaths).toBeUndefined()
  })
})

/**
 * A call that the reader refused never ran.
 *
 * Fast Agent asks the reader through `session/request_permission`. On a Deny answer, its
 * permission adapter writes one sentence (`ACPToolPermissionAdapter.check_permission`):
 * `The user has declined permission to use this tool: <server>__<tool>`, or
 * `The user has permanently declined permission to use this tool: <server>__<tool>` for
 * a refusal that it remembers. It then sends a failed update whose content is that
 * sentence alone (`ACPToolProgressManager.on_tool_permission_denied`). The update carries
 * no refusal field, so the exact sentence is the one native signal.
 */
describe('fast agent refused tool calls', () => {
  /** The pending call and the failed update that refuses it, read as one span. */
  function refused(opening: Record<string, unknown>, text: string, status = 'failed') {
    const ending = { sessionUpdate: 'tool_call_update', toolCallId: opening.toolCallId, status, content: acpTextContent(text) }
    return providerToolCall(AgentProvider.FAST_AGENT, ending, { request: input({ sessionUpdate: 'tool_call', status: 'pending', content: [], ...opening }, null, AgentProvider.FAST_AGENT) as ParsedMessageContent, spanType: 'tool_call_update' })
  }

  // `write_text_file` takes the arguments `{ path, content_length }`. They state the file
  // and no text to write (`ACPFilesystemRuntime.write_text_file`). A call that the model
  // did not stream opens with them. A streamed call opens with no arguments. The worker
  // then folds the arguments of the permission request into the request row (see the
  // next block).
  const WRITE = { toolCallId: 'fast-write', title: 'write_text_file', kind: 'edit', rawInput: { path: '/w/notes.txt', content_length: 12 } }
  const WRITE_REFUSAL = 'The user has declined permission to use this tool: acp_filesystem__write_text_file'

  it('reads a refused write as declined, with its file and the refusal as the result', () => {
    const call = refused(WRITE, WRITE_REFUSAL)
    expect(call?.degradation).toBeUndefined()
    expect(call?.kind).toBe('edit')
    expect(call?.status).toBe('declined')
    expect(call?.kind === 'edit' ? call.request.changes : []).toStrictEqual([
      { filePath: '/w/notes.txt', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null, showLineNumbers: false },
    ])
    expect(call?.result).toStrictEqual(failedResult(WRITE_REFUSAL))
    expect(call?.images).toStrictEqual([])
  })

  it('reads a refusal that Fast Agent remembers as declined', () => {
    const text = 'The user has permanently declined permission to use this tool: acp_terminal__execute'
    const call = refused({ toolCallId: 'fast-shell', title: 'execute', kind: 'execute', rawInput: { command: 'rm -f doomed.txt' } }, text)
    expect(call?.kind).toBe('execute')
    expect(call?.status).toBe('declined')
    expect(call?.result).toStrictEqual(failedResult(text))
    expect(call?.degradation).toBeUndefined()
  })

  it('reads a refused MCP tool as declined', () => {
    const text = 'The user has declined permission to use this tool: permission_probe__touch'
    const call = refused({ toolCallId: 'fast-mcp', title: 'permission_probe/touch', kind: 'other', rawInput: {} }, text)
    expect(call?.status).toBe('declined')
    expect(call?.result).toStrictEqual(failedResult(text))
    expect(call?.degradation).toBeUndefined()
  })

  // A refusal is the WHOLE text. The same words inside a longer text are a failure of
  // the tool's own.
  it.each([
    ['words before the sentence', `Error: ${WRITE_REFUSAL}`],
    ['words after the tool', `${WRITE_REFUSAL} because the reader was away`],
    ['a second line after the sentence', `${WRITE_REFUSAL}\nTry again.`],
  ])('keeps a failure that quotes the refusal with %s failed', (_case, text) => {
    expect(refused(WRITE, text)?.status).toBe('failed')
  })

  // Fast Agent writes the first sentence when the permission request itself is
  // cancelled, and the second when a permission handler denies a file call and gives no
  // message. Neither one is the reader's Deny answer.
  it.each([
    'Permission request cancelled',
    'Permission denied for writing file: /w/notes.txt',
  ])('keeps the failure %j failed', (text) => {
    expect(refused(WRITE, text)?.status).toBe('failed')
  })

  // A call that ran and PRINTED the words is a call that completed.
  it('keeps a completed call that printed the refusal words completed', () => {
    expect(refused(WRITE, WRITE_REFUSAL, 'completed')?.status).toBe('completed')
  })
})

/**
 * A refused write that the model STREAMED.
 *
 * The stream opens the call before the arguments arrive, so the opening frame states no
 * input (`ACPToolProgressManager._send_stream_start_notification`). The path then reaches
 * the client in a content diff, which the refusal replaces, and in the permission
 * request. The worker folds the input of the permission request into the supplement of
 * the stored request row (`conversation.notePermissionToolCall`). These frames are the
 * frames that Fast Agent 0.10.42 sent to a client that refused the write.
 */
describe('a refused streamed write', () => {
  const REFUSAL = 'The user has declined permission to use this tool: acp_filesystem__write_text_file'
  const opening = { sessionUpdate: 'tool_call', toolCallId: 'fast-write', title: 'write_text_file', kind: 'edit', status: 'pending', content: [] }
  const supplement = { sessionUpdate: 'tool_call', toolCallId: 'fast-write', status: 'pending', rawInput: { path: '/w/fa-local.txt', content_length: 16 } }
  const ending = { sessionUpdate: 'tool_call_update', toolCallId: 'fast-write', status: 'failed', content: acpTextContent(REFUSAL) }
  const stored = (frame: Record<string, unknown>, supplementalContent?: Record<string, unknown>): ParsedMessageContent => ({ rawText: '', topLevel: frame, parentObject: frame, wrapper: null, supplementalContent })
  const changes = [{ filePath: '/w/fa-local.txt', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null, showLineNumbers: false }]

  it.each([
    ['request', opening, { supplementalContent: supplement, result: stored(ending) }],
    ['result', ending, { request: stored(opening, supplement) }],
  ] as const)('reads the %s row as a declined edit of the file that the permission request stated', (role, frame, sides) => {
    const call = providerToolCall(AgentProvider.FAST_AGENT, frame, { role, spanType: 'edit', ...sides })
    expect(call?.degradation).toBeUndefined()
    expect(call?.kind).toBe('edit')
    expect(call?.status).toBe('declined')
    expect(call?.kind === 'edit' ? call.request.changes : []).toStrictEqual(changes)
    expect(call?.result).toStrictEqual(failedResult(REFUSAL))
  })

  // Without the supplement, the call states no file and the row degrades to the
  // uncategorized card. The worker stored this row before it read the permission request.
  it('degrades the refused write when no frame states its file', () => {
    const call = providerToolCall(AgentProvider.FAST_AGENT, ending, { request: stored(opening), spanType: 'edit' })
    expect(call?.kind).toBe('other')
    expect(call?.degradation).toStrictEqual({ fault: 'a-file-change-states-no-file', originalKind: 'edit' })
    expect(call?.status).toBe('declined')
  })
})
