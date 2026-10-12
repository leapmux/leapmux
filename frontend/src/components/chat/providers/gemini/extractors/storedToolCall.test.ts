import { describe, expect, it } from 'vitest'
import { geminiStoredToolCall } from './storedToolCall'

describe('geminiStoredToolCall', () => {
  it('preserves the committed native edit when its bytes differ from the requested edit', () => {
    const before = 'function value() {\n\treturn 41;\n}\n'
    const after = 'function value() {\n\treturn 42;\n\n}\n'
    const record = {
      id: 'replace__gemini-probe-replace',
      name: 'replace',
      args: { file_path: '/native/native-edit.txt', instruction: 'Change the return value from 41 to 42 and preserve the indentation.', old_string: '    return 41;\n', new_string: '    return 42;\n' },
      status: 'success',
      result: [{ functionResponse: { id: 'replace__gemini-probe-replace', name: 'replace', response: { output: `Successfully modified file: /native/native-edit.txt (1 replacements). Here is the updated code:\n${after}` } } }],
      resultDisplay: { fileDiff: 'Index: native-edit.txt\n===================================================================\n--- native-edit.txt\tCurrent\n+++ native-edit.txt\tProposed\n@@ -1,3 +1,4 @@\n function value() {\n-\treturn 41;\n+\treturn 42;\n+\n }\n', fileName: 'native-edit.txt', filePath: '/native/native-edit.txt', originalContent: before, newContent: after, isNewFile: false, isBuildFile: false },
    }
    const call = geminiStoredToolCall(record)
    expect(call.kind).toBe('edit')
    expect(call.request).toMatchObject({ changes: [{ oldStr: record.args.old_string, newStr: record.args.new_string }] })
    expect(call.result).toMatchObject({ changes: [{ filePath: '/native/native-edit.txt', oldStr: before, newStr: after }] })
    expect(JSON.stringify(call.result)).not.toBe(JSON.stringify(call.request))
  })

  it('draws the successful native submitted response as a final report', () => {
    const record = { id: 'complete_task__native-completion', name: 'complete_task', status: 'success', args: { result: { response: 'Complete native findings.' } }, resultDisplay: 'Output submitted and task completed.', result: [{ functionResponse: { response: { output: 'Output submitted and task completed.' } } }] }
    const call = geminiStoredToolCall(record)
    expect(call.kind).toBe('report')
    expect(call).toMatchObject({ status: 'completed', request: { payload: { response: 'Complete native findings.' } }, result: { text: 'Complete native findings.' } })
    expect(record).not.toHaveProperty('sessionUpdate')
  })

  it('preserves native structured findings and explicit empty submitted text', () => {
    const complete = (result: unknown) => geminiStoredToolCall({ id: 'complete_task__native-completion', name: 'complete_task', status: 'success', args: { result }, result: [{ functionResponse: { response: { output: 'Output submitted and task completed.' } } }] })
    expect(complete({ response: '' })).toMatchObject({ kind: 'report', status: 'completed', result: { text: '', format: 'markdown' } })
    const structured = complete({ count: 0, enabled: false, items: [] })
    expect(structured.kind).toBe('report')
    if (structured.kind !== 'report' || !structured.result || !('text' in structured.result))
      throw new Error('The native structured completion has no report text.')
    expect(JSON.parse(structured.result.text)).toEqual({ count: 0, enabled: false, items: [] })
  })

  it('shows the native validation failure instead of unaccepted candidate findings', () => {
    const call = geminiStoredToolCall({ id: 'complete_task__native-completion', name: 'complete_task', status: 'error', args: { result: { response: 'UNACCEPTED_NATIVE_FINDINGS' } }, result: [{ functionResponse: { response: { error: 'Output validation failed.' } } }] })
    expect(call).toMatchObject({ kind: 'report', status: 'failed', result: { failure: true, text: 'Output validation failed.' } })
    expect(JSON.stringify(call.result)).not.toContain('UNACCEPTED_NATIVE_FINDINGS')
  })

  it('keeps requested file bytes separate when the native result supplies no committed diff', () => {
    const call = geminiStoredToolCall({ id: 'write_file__native-write', name: 'write_file', status: 'success', args: { file_path: '/native/file.txt', content: 'REQUESTED_NATIVE_CONTENT' }, resultDisplay: 'Write completed.', result: [{ functionResponse: { response: { output: 'Write completed.' } } }] })
    expect(call.request).toMatchObject({ changes: [{ newStr: 'REQUESTED_NATIVE_CONTENT' }] })
    expect(call.result).toEqual({ unparsed: true, text: 'Write completed.' })
    expect(JSON.stringify(call.result)).not.toContain('REQUESTED_NATIVE_CONTENT')
  })
})
