import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { fastAgentNativeOutput, fastAgentTerminalOutputFileLimit } from './nativeToolOutput'

describe('fastAgentNativeOutput', () => {
  const path = resolve('native-full-output-fixture', 'fast-agent-output-random', 'output-0.log')
  const text = `head\n[Output truncated: The complete output is available during this session at ${path}. Use read_text_file for selected line ranges or run a targeted search against that file; avoid reading the entire file unless necessary.]\ntail`

  it('reads only the complete installed shell reference', () => {
    expect(fastAgentNativeOutput(text)).toBe(path)
    expect(fastAgentNativeOutput(text.replace('output-0.log', 'output-12.log'))).toBe(path.replace('output-0.log', 'output-12.log'))
  })

  it.each(['', 'The complete output can be read through the managed process handle.', text + text, text.replace('The complete output', 'The first 20 bytes of the output')])('rejects an absent, duplicate, partial, or process-only reference %j', (value) => {
    expect(() => fastAgentNativeOutput(value)).toThrow('complete')
  })

  it.each(['output-0.log', '/private/native/fast-agent-output-random/../output-0.log', '/private/native/other/output-0.log', '/private/native/fast-agent-output-random/output--1.log', '/private/native/fast-agent-output-random/other.log', '/private/native/fast-agent-output-random/\0output-0.log'])('rejects a different native path %j', (value) => {
    expect(() => fastAgentNativeOutput(text.replace(path, value))).toThrow('path')
  })
})

describe('fastAgentTerminalOutputFileLimit', () => {
  const text = '[Output truncated by ACP terminal outputByteLimit: 16000 bytes (~4848 tokens). Client returned partial output only.]\nnative tail42\n\n[Exit code: 0]'
  const frame = { sessionUpdate: 'tool_call_update', toolCallId: 'actual-acp-uuid', status: 'completed', rawInput: { command: 'native command' }, rawOutput: text, content: [{ type: 'content', content: { type: 'text', text } }] }

  it('keeps the actual ACP identity when it differs from the model call ID', () => {
    expect(fastAgentTerminalOutputFileLimit([frame], 'native command', text)).toEqual({ callId: 'actual-acp-uuid', text, previewText: '[Output truncated by ACP terminal outputByteLimit: 16000 bytes (~4848 tokens). Client returned partial output only.]\nnative tail42', byteLimit: 16000 })
  })

  it('rejects another command, result, or repeated completion', () => {
    expect(() => fastAgentTerminalOutputFileLimit([frame], 'other command', text)).toThrow('completed')
    expect(() => fastAgentTerminalOutputFileLimit([frame], 'native command', 'other text')).toThrow('completed')
    expect(() => fastAgentTerminalOutputFileLimit([frame, frame], 'native command', text)).toThrow('completed')
  })

  it('rejects malformed, failed, or complete local output instead of claiming the client limit', () => {
    expect(() => fastAgentTerminalOutputFileLimit([{ ...frame, status: 'failed' }], 'native command', text)).toThrow('completed')
    const different = text.replace('Client returned partial output only.', 'Complete output is available.')
    expect(() => fastAgentTerminalOutputFileLimit([{ ...frame, rawOutput: different, content: [{ type: 'content', content: { type: 'text', text: different } }] }], 'native command', different)).toThrow('limit')
    expect(() => fastAgentTerminalOutputFileLimit([{ ...frame, content: [] }], 'native command', text)).toThrow('completed')
  })

  const completeTerminalText = `[Output truncated by ACP terminal outputByteLimit: 128 bytes (~38 tokens). Client returned partial output only.]\n${'界😀'.repeat(100)}\n\n[Exit code: 0]`
  const terminalFrame = { ...frame, rawOutput: completeTerminalText, content: [{ type: 'content', content: { type: 'text', text: completeTerminalText } }] }
  const bytes = new TextEncoder().encode(completeTerminalText)
  const head = new TextDecoder().decode(bytes.slice(0, 64))
  const tail = new TextDecoder().decode(bytes.slice(-64))
  const notice = `[Tool result truncated: showing first 64 bytes and last 64 bytes of ${bytes.length} bytes (~38 of ~${Math.floor(bytes.length / 3.3)} tokens); omitted ${bytes.length - 128} middle bytes. Use a narrower query or request a smaller result to retain the relevant content.]`
  const modelText = `${head.endsWith('\n') ? head : `${head}\n`}${notice}${tail.startsWith('\n') ? tail : `\n${tail}`}`

  it('correlates the native terminal text with the exact second truncation in model history', () => {
    const receipt = fastAgentTerminalOutputFileLimit([terminalFrame], 'native command', modelText)
    expect(receipt).toMatchObject({ callId: frame.toolCallId, text: completeTerminalText, byteLimit: 128 })
    expect(receipt.previewText).toBe(`[Output truncated by ACP terminal outputByteLimit: 128 bytes (~38 tokens). Client returned partial output only.]\n${'界😀'.repeat(100)}`)
  })

  it.each([
    ['foreign prefix', modelText.replace(head, 'foreign bytes')],
    ['foreign suffix', modelText.replace(tail, 'foreign bytes')],
    ['incorrect total size', modelText.replace(`of ${bytes.length} bytes`, `of ${bytes.length + 1} bytes`)],
    ['incorrect omission', modelText.replace(`omitted ${bytes.length - 128}`, `omitted ${bytes.length - 127}`)],
    ['incorrect token count', modelText.replace('(~38 of', '(~39 of')],
    ['zero head size', modelText.replace('first 64 bytes', 'first 0 bytes')],
    ['negative head size', modelText.replace('first 64 bytes', 'first -1 bytes')],
    ['unequal window sizes', modelText.replace('last 64 bytes', 'last 63 bytes')],
    ['oversized window', modelText.replace('first 64 bytes', 'first 999999999999999999999 bytes')],
    ['duplicate notice', `${modelText}\n${notice}`],
    ['different guidance', modelText.replace('Use a narrower query', 'Read a output file')],
  ])('rejects a second truncation with %s', (_reason, invalid) => {
    expect(() => fastAgentTerminalOutputFileLimit([terminalFrame], 'native command', invalid)).toThrow('model result')
  })
})
