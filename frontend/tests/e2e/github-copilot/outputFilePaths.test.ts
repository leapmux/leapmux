import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT, COPILOT_METHOD } from '../../../src/generated/contracts/copilot-protocol'
import { copilotNativeOutputPaths, copilotNativePreview, copilotOutputFileCreatorPid } from './outputFilePaths'

const exit = { type: 'shell_exit', exitCode: 0, shellId: 'native-shell', outputFilePath: resolve('native-output-path-fixture', 'copilot-output.txt'), outputPreview: 'head', outputTruncated: true }
function frame(options: { callId?: string, sessionId?: string, success?: boolean, result?: Record<string, unknown> } = {}): unknown {
  return { jsonrpc: '2.0', method: COPILOT_METHOD.SessionEvent, params: { sessionId: options.sessionId ?? 'session', event: { type: COPILOT_EVENT.ToolCompleted, data: { toolCallId: options.callId ?? 'call', success: options.success ?? true, result: options.result ?? { content: 'model excerpt', detailedContent: 'native detailed text', contents: [exit] } } } } }
}

describe('copilotOutputFileCreatorPid', () => {
  const filename = '1790904357508-copilot-tool-output-96491-8e42bfbf-7faf-4a1e-adb4-53adf8e38b1b.txt'
  it('reads the exact PID from the observed native output filename', () => {
    expect(copilotOutputFileCreatorPid(resolve('native-output-path-fixture', filename))).toBe(96491)
  })
  it.each([
    filename.replace('-96491-', '-0-'),
    filename.replace('-96491-', '-9007199254740992-'),
    filename.replace('.txt', '.log'),
    `prefix-${filename}`,
    `${filename}.backup`,
    'copilot-output.txt',
  ])('rejects a malformed or ambiguous native creator: %s', (value) => {
    expect(() => copilotOutputFileCreatorPid(resolve('native-output-path-fixture', value))).toThrow('creator PID')
  })
})

describe('copilotNativeOutputPaths', () => {
  it('keeps the actual shell reference and separate inline display text', () => {
    expect(copilotNativeOutputPaths([frame()], 'call', 'session')).toEqual({ path: exit.outputFilePath, excerpt: 'model excerpt', preview: 'head', retained: 'native detailed text', shellId: exit.shellId })
  })

  it('preserves empty detailed text rather than replacing it with the excerpt', () => {
    expect(copilotNativeOutputPaths([frame({ result: { content: 'excerpt', detailedContent: '', contents: [exit] } })], 'call', 'session').retained).toBe('')
    expect(copilotNativeOutputPaths([frame({ result: { content: 'excerpt', contents: [exit] } })], 'call', 'session').retained).toBe('excerpt')
  })

  it.each([
    { shell: { type: 'shell_exit', exitCode: 0, shellId: 'native-shell', outputFilePath: exit.outputFilePath } },
    { shell: { type: 'shell_exit', exitCode: 0, shellId: 'native-shell', outputFilePath: exit.outputFilePath, outputPreview: '' } },
    { shell: { type: 'shell_exit', exitCode: 0, shellId: 'native-shell', outputFilePath: exit.outputFilePath, outputTruncated: true } },
  ])('preserves a native file reference when optional preview fields are absent: %j', ({ shell }) => {
    const receipt = copilotNativeOutputPaths([frame({ result: { content: 'model excerpt', contents: [shell] } })], 'call', 'session')
    expect(receipt.path).toBe(exit.outputFilePath)
    expect(receipt.excerpt).toBe('model excerpt')
    expect(receipt.preview).toBe('outputPreview' in shell ? shell.outputPreview : undefined)
  })

  it('keeps a native file reference when the optional flag reports a complete preview', () => {
    const receipt = copilotNativeOutputPaths([frame({ result: { content: 'model excerpt', contents: [{ ...exit, outputTruncated: false }] } })], 'call', 'session')
    expect(receipt.path).toBe(exit.outputFilePath)
    expect(receipt.preview).toBe(exit.outputPreview)
  })

  it('rejects absent, duplicate, failed, or foreign completions', () => {
    expect(() => copilotNativeOutputPaths([], 'call', 'session')).toThrow('complete')
    expect(() => copilotNativeOutputPaths([frame(), frame()], 'call', 'session')).toThrow('complete')
    expect(() => copilotNativeOutputPaths([frame({ success: false })], 'call', 'session')).toThrow('complete')
    expect(() => copilotNativeOutputPaths([frame({ callId: 'other' })], 'call', 'session')).toThrow('complete')
    expect(() => copilotNativeOutputPaths([frame({ sessionId: 'other' })], 'call', 'session')).toThrow('complete')
  })

  it.each([
    [],
    [exit, exit],
    [{ ...exit, outputTruncated: 'false' }],
    [{ ...exit, outputFilePath: 'relative.txt' }],
    [{ ...exit, exitCode: 1 }],
    [{ ...exit, shellId: '' }],
    [{ ...exit, outputPreview: null }],
  ].map(contents => ({ contents })))('rejects an invalid native shell-exit record %j', ({ contents }) => {
    expect(() => copilotNativeOutputPaths([frame({ result: { content: 'excerpt', contents } })], 'call', 'session')).toThrow('reference')
  })

  it('rejects a native child event even when its call ID matches', () => {
    const child = { jsonrpc: '2.0', method: COPILOT_METHOD.SessionEvent, params: { sessionId: 'session', event: { agentId: 'child', type: COPILOT_EVENT.ToolCompleted, data: { toolCallId: 'call', success: true, result: { content: 'excerpt', contents: [exit] } } } } }
    expect(() => copilotNativeOutputPaths([child], 'call', 'session')).toThrow('complete')
  })

  it('rejects nontext detailed output and empty identity', () => {
    expect(() => copilotNativeOutputPaths([frame({ result: { content: 'excerpt', contents: [exit], detailedContent: null } })], 'call', 'session')).toThrow('text')
    expect(() => copilotNativeOutputPaths([frame()], '', 'session')).toThrow('IDs')
  })
})

describe('copilotNativePreview', () => {
  it('removes only the final native shell trailer and preserves a literal earlier trailer', () => {
    const receipt = copilotNativeOutputPaths([frame({ result: { content: 'model excerpt', detailedContent: 'native text\n<shellId: literal completed with exit code 3>\nmore text\n<shellId: native-shell completed with exit code 0>', contents: [exit] } })], 'call', 'session')
    expect(copilotNativePreview(receipt)).toBe('native text\n<shellId: literal completed with exit code 3>\nmore text')
  })
})
