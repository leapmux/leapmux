import { describe, expect, it } from 'vitest'
import { acpToolCall } from '../../acp/extractors/toolCall'
import { junieToolCallAdapter } from './toolCall'

/**
 * Junie's `search_replace` as its ACP frame carries it: the model's own
 * arguments under `rawInput`, with the file path under `file_path` and the two
 * sides of the substitution under `search`/`replace`.
 */
function searchReplaceCall() {
  return acpToolCall(
    {
      sessionUpdate: 'tool_call',
      toolCallId: 'junie-edit',
      status: 'pending',
      title: 'Edit note.txt',
      kind: 'edit',
      rawInput: { file_path: '/w/note.txt', search: 'junie-before', replace: 'junie-after' },
    },
    junieToolCallAdapter,
    undefined,
  )
}

describe('junieToolCallAdapter', () => {
  it('states the file and both sides of a search_replace edit', () => {
    const call = searchReplaceCall()
    expect(call.kind).toBe('edit')
    if (call.kind !== 'edit')
      return
    expect(call.request.changes).toHaveLength(1)
    const [change] = call.request.changes
    expect(change?.filePath).toBe('/w/note.txt')
    expect(change?.oldStr).toBe('junie-before')
    expect(change?.newStr).toBe('junie-after')
  })

  it('keeps a search_replace call a file change rather than the uncategorized row', () => {
    // A blank filePath degrades the row and the renderer loses the diff; the
    // mapping must always state a non-blank one.
    const call = searchReplaceCall()
    expect(call.kind).toBe('edit')
    expect(call.kind === 'other' ? call.request.args : null).toBeNull()
  })

  it('takes the change junie actually sends: a content diff and a location, no rawInput', () => {
    // Junie's ACP frame states the edit as a `{type:'diff'}` content entry plus
    // `locations`, and carries NO rawInput at all. The request must still name
    // the file and both sides, or the row degrades and the diff is lost.
    const call = acpToolCall(
      {
        sessionUpdate: 'tool_call',
        toolCallId: 'junie-edit',
        status: 'in_progress',
        title: 'Edit note.txt',
        kind: 'edit',
        content: [{ type: 'diff', path: '/w/note.txt', oldText: 'junie-before\n', newText: 'junie-after\n' }],
        locations: [{ path: '/w/note.txt' }],
      },
      junieToolCallAdapter,
      undefined,
    )
    expect(call.kind).toBe('edit')
    if (call.kind !== 'edit')
      return
    const [change] = call.request.changes
    expect(change?.filePath).toBe('/w/note.txt')
    expect(change?.oldStr).toBe('junie-before\n')
    expect(change?.newStr).toBe('junie-after\n')
  })
})
