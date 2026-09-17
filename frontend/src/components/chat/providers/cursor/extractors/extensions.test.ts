import type { ToolCallIR } from '../../../ir/toolCall'
import { describe, expect, it } from 'vitest'
import { CURSOR_METHOD, CURSOR_SUPPLEMENT } from '~/generated/contracts/cursor-protocol'
import { todoTitleOf } from '~/test-support/toolCallIr'
import { isFailedResult, typedResult } from '../../../ir/toolCall'

import { acpToolCallIR } from '../../acp/extractors/toolCall'
import { cursorToolCallAdapter } from './toolCall'

// Cursor's own call id carries an embedded newline. It is kept verbatim here because
// the row key is what the worker enriches, and a test that tidied it would not.
const CALL = 'call-9ff1787e-0\nfc_442cb271_0'

/**
 * One tool call, with the extension frame the worker stored on it.
 *
 * The supplement repeats the ACP identity fields, which is what the facts reader
 * needs to accept it -- see `resolveACPMessageContent` on the Go side for the same
 * rule.
 */
function callWithExtension(tool: Record<string, unknown>, method?: string, params?: Record<string, unknown>): ToolCallIR {
  const frame = { sessionUpdate: 'tool_call_update', toolCallId: CALL, status: 'completed', ...tool }
  const supplemental = method
    ? {
        sessionUpdate: frame.sessionUpdate,
        toolCallId: frame.toolCallId,
        status: frame.status,
        [CURSOR_SUPPLEMENT.Extension]: { method, params: { toolCallId: CALL, ...params } },
      }
    : undefined
  return acpToolCallIR(frame, cursorToolCallAdapter, supplemental)
}

describe('cursor updateTodos rows', () => {
  it('answers with the list the stored frame carries', () => {
    const call = callWithExtension(
      { kind: 'other', title: 'Update TODOs', rawInput: { _toolName: 'updateTodos' } },
      CURSOR_METHOD.UpdateTodos,
      { todos: [{ id: '1', content: 'Create a.txt', status: 'in_progress' }, { id: '2', content: 'Create b.txt', status: 'pending' }], merge: false },
    )
    expect(call.kind).toBe('todo')
    expect(call.label).toBe('Update TODOs')
    // No title on the PAYLOAD: `todoRenderer` composes the words from the request the
    // call carries, so a copy here would be a second place for them to drift.
    expect(call.title).toBeUndefined()
    expect(todoTitleOf(call)).toBe('2 tasks')
    expect(call.kind === 'todo' ? typedResult(call)?.items : undefined).toEqual([
      { rowKey: '0:Create a.txt', content: 'Create a.txt', status: 'in_progress', activeForm: '' },
      { rowKey: '1:Create b.txt', content: 'Create b.txt', status: 'pending', activeForm: '' },
    ])
  })

  // A row written before the worker stored these frames carries the protobuf enum
  // NAME. Reading it through the shared normalizer alone made every such row pending.
  it('folds the protobuf enum name of a row with no stored frame', () => {
    const call = callWithExtension({
      kind: 'other',
      title: 'Update TODOs',
      rawInput: { _toolName: 'updateTodos', todos: [{ id: '1', content: 'Create a.txt', status: 'TODO_STATUS_COMPLETED' }] },
    })
    expect(call.kind === 'todo' ? typedResult(call)?.items : undefined).toMatchObject([{ content: 'Create a.txt', status: 'completed' }])
  })

  it('reads a cancelled task as the deleted tombstone', () => {
    const call = callWithExtension(
      { kind: 'other', rawInput: { _toolName: 'updateTodos' } },
      CURSOR_METHOD.UpdateTodos,
      { todos: [{ id: '1', content: 'Stop', status: 'cancelled' }], merge: true },
    )
    expect(call.kind === 'todo' ? typedResult(call)?.items : undefined).toMatchObject([{ status: 'deleted' }])
  })

  // The opening call states the tool name and nothing else, so there is no list to
  // draw yet and the call must not claim an empty one.
  it('carries no list before the call finishes', () => {
    const call = callWithExtension({ kind: 'other', status: 'in_progress', rawInput: { _toolName: 'updateTodos' } })
    expect(call.kind).toBe('todo')
    expect(call.result).toBeUndefined()
    expect(call.title).toBeUndefined()
    expect(todoTitleOf(call)).toBe('To-do list')
  })
})

describe('cursor generateImage rows', () => {
  // `filePath` is where the runtime WROTE the image. `rawInput.filename` is only what
  // the call asked for, and the runtime does not have to honor it.
  it('shows the produced image and then the references', () => {
    const call = callWithExtension(
      { kind: 'other', title: 'Generate Image: A cat...', rawInput: { _toolName: 'generateImage', description: 'A cat', filename: 'wanted.png' } },
      CURSOR_METHOD.GenerateImage,
      { description: 'A cat', filePath: '/tmp/cat.png', referenceImagePaths: ['/tmp/ref.png'] },
    )
    expect(call.label).toBe('Generate Image')
    expect(call.title).toBe('A cat')
    expect(call.images).toEqual([
      { filePath: '/tmp/cat.png', description: 'A cat' },
      { filePath: '/tmp/ref.png', description: 'Reference image' },
    ])
  })

  it('shows no image for a row whose frame never arrived', () => {
    const call = callWithExtension({ kind: 'other', rawInput: { _toolName: 'generateImage', description: 'A cat' } })
    expect(call.images).toEqual([])
    expect(call.title).toBe('A cat')
  })

  /*
   * `ImageRequest.prompt` is OPTIONAL, and `imageRenderer` reads
   * `call.request.prompt ?? call.title`. `pickString` answers `''` for an absent
   * description, and `'' ?? x` is `''` -- so the header drew `Generate Image` above an
   * empty title and the frame's own title could never be reached.
   */
  it('leaves the prompt absent so the frame title reaches the header', () => {
    const call = callWithExtension({ kind: 'other', title: 'Generate Image: a cat', rawInput: { _toolName: 'generateImage' } })
    expect(call.kind).toBe('image')
    expect(call.kind === 'image' ? call.request.prompt : 'set').toBeUndefined()
    expect(call.title).toBe('Generate Image: a cat')
  })
})

describe('cursor askQuestion rows', () => {
  it('states the question and the choices that were offered', () => {
    const call = callWithExtension({
      kind: 'think',
      title: 'Pick a parser',
      rawInput: {
        _toolName: 'askQuestion',
        title: 'Pick a parser',
        questions: [{ id: 'q1', prompt: 'Which one?', options: [{ id: 'a', label: 'Recursive descent' }, { id: 'b', label: 'PEG' }] }],
      },
    })
    expect(call.kind).toBe('question')
    expect(call.label).toBe('Ask Question')
    expect(call.title).toBe('Pick a parser')
    expect(call.kind === 'question' ? call.request.questions : undefined).toEqual([
      { header: undefined, question: 'Which one?', options: [{ label: 'Recursive descent', description: undefined }, { label: 'PEG', description: undefined }] },
    ])
  })

  it('asks nothing when the call carried no question', () => {
    const call = callWithExtension({ kind: 'think', rawInput: { _toolName: 'askQuestion', title: 'Pick a parser' } })
    expect(call.kind === 'question' ? call.request.questions : undefined).toEqual([])
  })
})

describe('cursor task rows', () => {
  // The call carries the prompt, the description and the REQUESTED type. The frame
  // adds the model that answered, the agent id and the measured duration.
  it('reports the run and not only the request', () => {
    const call = callWithExtension(
      { kind: 'other', title: 'Task: Explore', rawInput: { _toolName: 'task', description: 'Explore', prompt: 'Find the parser' } },
      CURSOR_METHOD.Task,
      { description: 'Explore', prompt: 'Find the parser', subagentType: 'explore', model: 'composer-2.5', agentId: 'a-1', durationMs: 1200 },
    )
    expect(call.kind).toBe('agent')
    const source = call.kind === 'agent' ? typedResult(call)?.agents[0] : undefined
    expect(source?.agentId).toBe('a-1')
    expect(source?.metadata).toEqual([
      { label: 'Agent ID', value: 'a-1' },
      { label: 'Model', value: 'composer-2.5' },
      { label: 'Duration', value: '1.2s' },
    ])
  })

  it('identifies a snake_case subagent type the way the reader knows it', () => {
    const call = callWithExtension(
      { kind: 'other', rawInput: { _toolName: 'task', description: 'Drive the UI' } },
      CURSOR_METHOD.Task,
      { subagentType: 'computer_use' },
    )
    expect(call.kind === 'agent' ? typedResult(call)?.agents[0]?.description : undefined).toBe('Drive the UI')
    // The TYPE is what this case is about. `AGENT_TYPES` folds the snake_case word the
    // extension frame sends to the words the reader knows, and the request carries it.
    expect(call.kind === 'agent' ? call.request.agentType : undefined).toBe('Computer use')
  })
})

describe('cursor declined rows', () => {
  // A refused approval means the tool never ran, which `failed` would misreport as a
  // tool that tried. Cursor writes the reason on a web search and a web fetch.
  it('reads a rejected approval as declined and keeps its reason', () => {
    const call = callWithExtension({ kind: 'search', title: 'Web Search: parsers', rawOutput: { rejected: true, reason: 'User Rejected' } })
    expect(call.status).toBe('declined')
    expect(isFailedResult(call.result) && call.result.text).toBe('User Rejected')
  })

  // An MCP call states no reason, so the call keeps whatever text it already had.
  it('reads a policy refusal as declined', () => {
    const call = callWithExtension({ kind: 'other', title: 'mcp_docs_search', rawOutput: { permissionDenied: true } })
    expect(call.status).toBe('declined')
  })

  // The refused call's own frame stays at `completed`; the declined status is the
  // call's, and the card must not claim the call succeeded under it.
  it('does not let an MCP call claim the refused call succeeded', () => {
    const call = callWithExtension({
      kind: 'other',
      status: 'completed',
      rawInput: { providerIdentifier: 'docs', toolName: 'search', args: { q: 'needle' } },
      rawOutput: { rejected: true, reason: 'User Rejected' },
    })
    expect(call.status).toBe('declined')
    expect(call.kind).toBe('mcp')
    expect(isFailedResult(call.result) && call.result.text).toBe('User Rejected')
  })

  it('leaves an ordinary result alone', () => {
    const call = callWithExtension({ kind: 'other', title: 'Web Search: parsers', rawOutput: { referenceCount: 3 } })
    expect(call.status).toBe('completed')
  })
})
