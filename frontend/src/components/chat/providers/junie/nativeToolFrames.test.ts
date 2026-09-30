import { describe, expect, it } from 'vitest'
import { acpToolCall } from '../acp/extractors/toolCall'

describe('Junie native tool frames', () => {
  it('reads the search_replace request from the model input', () => {
    const call = acpToolCall({
      sessionUpdate: 'tool_call',
      toolCallId: 'junie-edit',
      status: 'pending',
      title: 'Edit note.txt',
      kind: 'edit',
      rawInput: { file_path: '/w/note.txt', search: 'junie-before', replace: 'junie-after' },
    }, undefined, undefined)

    expect(call.kind).toBe('edit')
    if (call.kind !== 'edit')
      return
    expect(call.request.changes[0]).toMatchObject({
      filePath: '/w/note.txt',
      oldStr: 'junie-before',
      newStr: 'junie-after',
    })
  })

  it('reads a confirmed diff when Junie supplies no raw input', () => {
    const call = acpToolCall({
      sessionUpdate: 'tool_call',
      toolCallId: 'junie-edit',
      status: 'in_progress',
      title: 'Edit note.txt',
      kind: 'edit',
      content: [{ type: 'diff', path: '/w/note.txt', oldText: 'junie-before\n', newText: 'junie-after\n' }],
      locations: [{ path: '/w/note.txt' }],
    }, undefined, undefined)

    expect(call.kind).toBe('edit')
    if (call.kind !== 'edit')
      return
    expect(call.request.changes[0]).toMatchObject({
      filePath: '/w/note.txt',
      oldStr: 'junie-before\n',
      newStr: 'junie-after\n',
    })
  })
})
