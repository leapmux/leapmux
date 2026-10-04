/** The installed native edit preserves a tab and adds a blank line beyond the requested replacement. */
export const geminiNativeCommittedEdit = {
  id: 'replace__gemini-probe-replace',
  name: 'replace',
  args: {
    file_path: '/native/native-edit.txt',
    instruction: 'Change the return value from 41 to 42 and preserve the indentation.',
    old_string: '    return 41;\n',
    new_string: '    return 42;\n',
  },
  result: [
    {
      functionResponse: {
        id: 'replace__gemini-probe-replace',
        name: 'replace',
        response: {
          output: 'Successfully modified file: /native/native-edit.txt (1 replacements). Here is the updated code:\nfunction value() {\n\treturn 42;\n\n}\n',
        },
      },
    },
  ],
  status: 'success',
  timestamp: '2026-10-02T21:04:44.513Z',
  resultDisplay: {
    fileDiff: 'Index: native-edit.txt\n===================================================================\n--- native-edit.txt\tCurrent\n+++ native-edit.txt\tProposed\n@@ -1,3 +1,4 @@\n function value() {\n-\treturn 41;\n+\treturn 42;\n+\n }\n',
    fileName: 'native-edit.txt',
    filePath: '/native/native-edit.txt',
    originalContent: 'function value() {\n\treturn 41;\n}\n',
    newContent: 'function value() {\n\treturn 42;\n\n}\n',
    diffStat: {
      model_added_lines: 2,
      model_removed_lines: 1,
      model_added_chars: 11,
      model_removed_chars: 11,
      user_added_lines: 0,
      user_removed_lines: 0,
      user_added_chars: 0,
      user_removed_chars: 0,
    },
    isNewFile: false,
    isBuildFile: false,
  },
  description: 'native-edit.txt:     return 41; =>     return 42;',
  displayName: 'Edit',
  renderOutputAsMarkdown: true,
}
