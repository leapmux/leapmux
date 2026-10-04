import { describe, expect, it } from 'vitest'
import { diracNativeLogPath, diracNativeOutputPaths } from './outputFilePaths'

const script = 'console.log(21 * 2)'
const path = '/native/tmp/dirac/large-output-123-abc.log'
const preview = `native head\n[output truncated]\nnative tail\nFull output saved to: ${path}`
function frames() {
  return [
    { original: { sessionUpdate: 'tool_call', toolCallId: 'generated', name: 'execute_command', status: 'in_progress', rawInput: { tool: 'execute_command', language: 'node', displayName: 'Node script', command: `node << 'EOF_DIRAC_SCRIPT_A1'\n${script}\nEOF_DIRAC_SCRIPT_A1` } }, supplemental: undefined },
    { original: { sessionUpdate: 'tool_call_update', toolCallId: 'generated', name: 'execute_command', status: 'completed', rawOutput: { output: preview, exitCode: 0, userRejected: false } }, supplemental: undefined },
  ]
}

describe('diracNativeLogPath', () => {
  it('reads the complete final native pointer and retains its spelling', () => {
    expect(diracNativeLogPath(preview)).toBe(path)
  })

  it.each([undefined, null, false, 0, '', `Full output saved to: relative/dirac/large-output-123-abc.log`, `${preview}\nother text`, `${preview}\nFull output saved to: ${path}`, preview.replace(path, 'file:///native/log')])('refuses a malformed or ambiguous native pointer: %j', (value) => {
    expect(diracNativeLogPath(value)).toBeUndefined()
  })
})

describe('diracNativeOutputPaths', () => {
  it('keeps exact script, command, zero exit, paths, and native preview without a saved body', () => {
    const native = frames()
    const before = JSON.stringify(native)
    expect(diracNativeOutputPaths(native, script, '/native/tmp')).toEqual({ callId: 'generated', output: preview, previewText: preview, paths: [path], exitCode: 0, failed: false })
    expect(JSON.stringify(native)).toBe(before)
  })

  it('refuses another command, private temp root, or duplicate completion', () => {
    expect(() => diracNativeOutputPaths(frames(), 'another script', '/native/tmp')).toThrow('exact generated')
    expect(() => diracNativeOutputPaths(frames(), script, '/foreign/tmp')).toThrow('another runtime')
    const duplicate = frames()
    duplicate.push(duplicate[1]!)
    expect(() => diracNativeOutputPaths(duplicate, script, '/native/tmp')).toThrow('exact completion')
  })
})
