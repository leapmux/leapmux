/** The installed Command Code 1.73.2 matched smart quotes and preserved the file's BOM and CRLF bytes. */
export const commandCodeNativeFuzzyEditRequest = {
  type: 'event',
  seq: 20,
  event: {
    type: 'tool_queued',
    toolCallId: 'native-fuzzy-edit',
    toolName: 'edit_file',
    input: { file_path: '/work/native-fuzzy-crlf.txt', old_string: 'const value = "old";\n', new_string: 'const value = "new";\n' },
  },
}

/** Native success reports the actual edited-region snippet. It supplies no complete old region. */
export const commandCodeNativeFuzzyEditResult = {
  type: 'event',
  seq: 22,
  event: {
    type: 'tool_completed',
    toolCallId: 'native-fuzzy-edit',
    toolName: 'edit_file',
    result: [{ type: 'text', text: 'Edited /work/native-fuzzy-crlf.txt (1 replacement)\n\nNote: old_string matched after normalizing smart quotes/dashes; the replacement keeps the file’s original punctuation style.\n\nSnippet (lines 1-2 of 2):\n1\tconst value = “new”;\n2\t' }],
    deferred: false,
  },
}
