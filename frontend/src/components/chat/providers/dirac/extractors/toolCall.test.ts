import { describe, expect, it } from 'vitest'
import { acpToolCall } from '../../acp/extractors/toolCall'
import { diracToolCallAdapter } from './toolCall'

describe('diracToolCallAdapter', () => {
  it('flattens an edit_file files[].edits[] call into one file change', () => {
    // Dirac's `edit_file` states its target as `files: [{path, edits: [...]}]`,
    // and each substitution names its line by an ANCHOR§CONTENT coordinate. The
    // shared builder reads one file with old/new substitutions, so the adapter
    // flattens the first file and takes the old text from the anchor's content.
    const call = acpToolCall(
      {
        sessionUpdate: 'tool_call',
        toolCallId: 'dirac-edit',
        status: 'pending',
        title: 'Edit note.txt',
        kind: 'edit',
        rawInput: {
          tool: 'edit_file',
          files: [{
            path: '/w/note.txt',
            edits: [{ edit_type: 'replace', anchor: 'Maintenance§dirac-before', end_anchor: 'Maintenance§dirac-before', text: 'dirac-after' }],
          }],
        },
      },
      diracToolCallAdapter,
      undefined,
    )
    expect(call.kind).toBe('edit')
    if (call.kind !== 'edit')
      return
    const [change] = call.request.changes
    expect(change?.filePath).toBe('/w/note.txt')
    expect(change?.oldStr).toBe('dirac-before')
    expect(change?.newStr).toBe('dirac-after')
  })

  it('takes a content diff when the call states its change that way', () => {
    const call = acpToolCall(
      {
        sessionUpdate: 'tool_call',
        toolCallId: 'dirac-edit',
        status: 'in_progress',
        title: 'Edit note.txt',
        kind: 'edit',
        content: [{ type: 'diff', path: '/w/note.txt', oldText: 'dirac-before\n', newText: 'dirac-after\n' }],
      },
      diracToolCallAdapter,
      undefined,
    )
    expect(call.kind).toBe('edit')
    if (call.kind !== 'edit')
      return
    const [change] = call.request.changes
    expect(change?.filePath).toBe('/w/note.txt')
    expect(change?.oldStr).toBe('dirac-before\n')
    expect(change?.newStr).toBe('dirac-after\n')
  })
})
