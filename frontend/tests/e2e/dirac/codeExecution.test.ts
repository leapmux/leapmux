import { describe, expect, it } from 'vitest'
import { diracNativeScriptCommand, diracScriptReceipt } from './codeExecution'

const script = 'console.log("native" + (40 + 2));'
const input = { tool: 'execute_command', language: 'node', displayName: 'Node script', command: `node << 'EOF_DIRAC_SCRIPT_A1B2'\n${script}\nEOF_DIRAC_SCRIPT_A1B2` }
const start = { sessionUpdate: 'tool_call', toolCallId: 'generated-card', rawInput: input }
const end = { sessionUpdate: 'tool_call_update', toolCallId: 'generated-card', status: 'completed', rawOutput: { output: 'native42\n', exitCode: 0 } }
const frames = [{ original: start, supplemental: undefined }, { original: end, supplemental: undefined }]

describe('diracNativeScriptCommand', () => {
  it('requires the complete native wrapper and exact unchanged script', () => {
    expect(diracNativeScriptCommand(input.command, script)).toBe(true)
    expect(diracNativeScriptCommand(input.command, script.slice(0, -1))).toBe(false)
    expect(diracNativeScriptCommand(`prefix ${input.command}`, script)).toBe(false)
    expect(diracNativeScriptCommand(`${input.command}\nextra`, script)).toBe(false)
    expect(diracNativeScriptCommand(input.command.replace('node <<', 'python3 <<'), script)).toBe(false)
    expect(diracNativeScriptCommand('', script)).toBe(false)
    expect(diracNativeScriptCommand(input.command, '')).toBe(false)
  })
})

describe('diracScriptReceipt', () => {
  it('reads the current native top-level tool name without a raw input tool field', () => {
    const { tool: _tool, ...nativeInput } = input
    const output = 'Command executed successfully (exit code 0).\nOutput:\nnative42'
    const nativeFrames = [
      { original: { ...start, name: 'execute_command', rawInput: nativeInput }, supplemental: undefined },
      { original: { ...end, name: 'execute_command', rawInput: nativeInput, rawOutput: { output, exitCode: 0 } }, supplemental: undefined },
    ]
    expect(diracScriptReceipt(nativeFrames, script)).toEqual({ callId: 'generated-card', output, exitCode: 0, failed: false })
  })

  it('preserves native request fields when the Worker supplement contains only ACP identity', () => {
    const { tool: _tool, ...nativeInput } = input
    const nativeFrames = [
      { original: { ...start, name: 'execute_command', rawInput: nativeInput }, supplemental: { provider: { sessionUpdate: 'tool_call', status: 'pending', toolCallId: start.toolCallId } } },
      { original: { ...end, name: 'execute_command', rawInput: nativeInput }, supplemental: { provider: { protocol: { kind: 'execute' }, sessionUpdate: 'tool_call_update', status: 'completed', toolCallId: end.toolCallId } } },
    ]
    expect(diracScriptReceipt(nativeFrames, script)).toEqual({ callId: 'generated-card', output: 'native42\n', exitCode: 0, failed: false })
  })

  it('uses the generated execution identity and actual successful output', () => {
    expect(diracScriptReceipt(frames, script)).toEqual({ callId: 'generated-card', output: 'native42\n', exitCode: 0, failed: false })
  })

  it('retains a failed process result and its exact exit code', () => {
    expect(diracScriptReceipt([frames[0]!, { original: { ...end, status: 'failed', rawOutput: { output: 'native77\n', exitCode: 7 } }, supplemental: undefined }], script))
      .toEqual({ callId: 'generated-card', output: 'native77\n', exitCode: 7, failed: true })
  })

  it('reads the native request supplement when only the completion remains', () => {
    expect(diracScriptReceipt([{ original: end, supplemental: { provider: start } }], script).output).toBe('native42\n')
  })

  it('preserves empty output and a zero exit code', () => {
    expect(diracScriptReceipt([frames[0]!, { original: { ...end, rawOutput: { output: '', exitCode: 0 } }, supplemental: undefined }], script).output).toBe('')
  })

  it.each([
    { output: 'native42' },
    { output: 'native42', exitCode: -1 },
    { output: 'native42', exitCode: 0.5 },
    { output: 'native42', exitCode: Number.NaN },
    { output: 'native42', exitCode: 0, signal: 'SIGTERM' },
    { output: 'native42', exitCode: 0, userRejected: true },
    { output: null, exitCode: 0 },
  ])('rejects an incomplete native process result %j', (rawOutput) => {
    expect(() => diracScriptReceipt([frames[0]!, { original: { ...end, rawOutput }, supplemental: undefined }], script)).toThrow('process result')
  })

  it.each([{ status: 'completed', exitCode: 7 }, { status: 'failed', exitCode: 0 }])('rejects a status mismatch %j', ({ status, exitCode }) => {
    expect(() => diracScriptReceipt([frames[0]!, { original: { ...end, status, rawOutput: { output: '', exitCode } }, supplemental: undefined }], script)).toThrow('disagrees')
  })

  it('rejects a supplement for another native card', () => {
    expect(() => diracScriptReceipt([{ original: end, supplemental: { provider: { ...start, toolCallId: 'foreign' } } }], script)).toThrow('another card')
  })

  it('rejects repeated completions and separate cards with the same script', () => {
    expect(() => diracScriptReceipt([...frames, frames[1]!], script)).toThrow('one exact completion')
    expect(() => diracScriptReceipt([...frames, { original: { ...start, toolCallId: 'second-card' }, supplemental: undefined }], script)).toThrow('one exact generated')
  })

  it('rejects absent input, wrong language, and a result for another call', () => {
    expect(() => diracScriptReceipt([], script)).toThrow('generated execution card')
    expect(() => diracScriptReceipt([{ original: { ...start, rawInput: { ...input, language: 'python' } }, supplemental: undefined }], script)).toThrow('generated execution card')
    expect(() => diracScriptReceipt([frames[0]!, { original: { ...end, toolCallId: 'foreign' }, supplemental: undefined }], script)).toThrow('one exact completion')
  })
})
