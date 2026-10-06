import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { describe, expect, it } from 'vitest'
import { nativeOutputSnapshot } from '../helpers/nativeOutputReaderCases'
import { readPiNativeOutput } from './outputFilePaths'

const full = 'first\nCOMPUTED_MIDDLE77\nlast42'
const path = '/private/pi-codemode-0123456789abcdef.txt'
const proof = { callId: 'full-output-call', toolName: 'codemode', agentId: 'native-agent', agentSessionId: 'native-session', expectedText: full, omittedMarker: 'COMPUTED_MIDDLE77' }
const header = 'Script completed\nWall time 0 seconds\nOutput:\n'
const frame = { type: 'tool_execution_end', toolCallId: proof.callId, toolName: proof.toolName, isError: false, result: { content: [{ type: 'text', text: header }, { type: 'text', text: 'first\n[preview]\nlast42' }], details: { calls: [], fullOutputPath: path } } }
const supplement = { provider: { toolCallId: proof.callId, toolName: proof.toolName, outputFile: { path, text: full } } }
/** The error of the shared record reader for a snapshot with no record of the call. */
const NO_RECORD = 'The native output requires exactly one accepted record in its Worker session and span.'
/** The error of the Pi reader for a failed result, another span type, or a result with no output path. */
const NO_POINTER = 'The native Pi result has no exact successful span and filesystem pointer.'
/** The error of the Pi reader for a result with no codemode header and preview. */
const NO_HEADER = 'The native Pi result has no successful codemode header and preview.'

function snapshot(value: unknown = frame, extra: unknown = supplement): NativeMessageSnapshot {
  return nativeOutputSnapshot([{ frame: value, spanId: proof.callId, spanType: proof.toolName, supplement: extra }], { agentId: proof.agentId, agentSessionId: proof.agentSessionId })
}

describe('readPiNativeOutput', () => {
  it('keeps the original preview and native header in the copied representation', () => {
    const input = snapshot()
    const before = JSON.stringify(input, (_key, value: unknown) => typeof value === 'bigint' ? value.toString() : value)
    const receipt = readPiNativeOutput(input, proof)
    expect(receipt.paths).toEqual([path])
    expect(receipt.excerpt).not.toContain(proof.omittedMarker)
    expect(receipt.previewText.startsWith(`${header}\n\nfirst\n[preview]\nlast42\n\n`)).toBe(true)
    expect(receipt.previewText).toContain('"calls"')
    expect(receipt.previewText).toContain(path)
    expect(JSON.stringify(input, (_key, value: unknown) => typeof value === 'bigint' ? value.toString() : value)).toBe(before)
  })

  // A frame of another call or tool is no record of the call. Each other defect reaches the error of its own check.
  it.each([
    [{ ...frame, toolCallId: 'foreign' }, NO_RECORD],
    [{ ...frame, toolName: 'read' }, NO_RECORD],
    [{ ...frame, isError: true }, NO_POINTER],
    [{ ...frame, isError: undefined }, NO_POINTER],
    [{ ...frame, result: {} }, NO_POINTER],
    [{ ...frame, result: { ...frame.result, details: {} } }, NO_POINTER],
    [{ ...frame, result: { ...frame.result, content: [{ type: 'text', text: full }] } }, NO_HEADER],
    [{ ...frame, result: { ...frame.result, content: [{ type: 'text', text: 'Script completed\nWall time .. seconds\nOutput:\n' }, { type: 'text', text: 'preview' }] } }, NO_HEADER],
  ])('rejects an incomplete or uncorrelated native completion %j', (value, error) => {
    expect(() => readPiNativeOutput(snapshot(value), proof)).toThrow(error)
  })

  it.each(['relative/pi-codemode-0123456789abcdef.txt', '/private/pi-mcp-0123456789abcdef.txt', '/private/pi-codemode-invalid.txt'])('rejects an unsupported native output path %s', (fullOutputPath) => {
    expect(() => readPiNativeOutput(snapshot({ ...frame, result: { ...frame.result, details: { ...frame.result.details, fullOutputPath } } }), proof)).toThrow('filesystem pointer')
  })

  it.each([{}, null, { provider: { outputFile: { path: '/foreign', text: 'FORGED_COMPLETE_BODY' } } }])('does not replace native preview text from a discarded feature supplement %j', (extra) => {
    const receipt = readPiNativeOutput(snapshot(frame, extra), proof)
    expect(receipt.previewText).not.toContain('FORGED_COMPLETE_BODY')
    expect(receipt.excerpt).toBe('first\n[preview]\nlast42')
  })

  it.each([{ agentId: 'other' }, { agentSessionId: 'other' }])('rejects a different native snapshot owner %j', (owner) => {
    expect(() => readPiNativeOutput({ ...snapshot(), ...owner }, proof)).toThrow('exact native session')
  })

  it('rejects duplicate native completion rows', () => {
    const input = snapshot()
    input.messages.push(input.messages[0]!)
    expect(() => readPiNativeOutput(input, proof)).toThrow('requires exactly one')
  })

  // The record reader selects by the span ID, and the Pi reader then requires the span type of the codemode tool.
  it.each([[{ spanId: 'other' }, NO_RECORD], [{ spanType: 'other' }, NO_POINTER]])('rejects a different Worker span %j', (span, error) => {
    const input = snapshot()
    Object.assign(input.messages[0]!, span)
    expect(() => readPiNativeOutput(input, proof)).toThrow(error)
  })

  it.each([{ ...proof, callId: '' }, { ...proof, agentId: '' }, { ...proof, agentSessionId: '' }])('rejects an absent output file proof field %j', (input) => {
    expect(() => readPiNativeOutput(snapshot(), input)).toThrow('exact native session')
  })
})
