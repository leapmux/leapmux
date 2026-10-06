import { describe, expect, it } from 'vitest'
import { KIMI_OUTPUT_PATH_CALL_IDS, kimiNativeOutputPointer } from './outputFilePaths'

describe('KIMI_OUTPUT_PATH_CALL_IDS', () => {
  // Kimi's OpenAI adapter replaces unsafe characters and caps each ID at 64 characters.
  it.each(Object.entries(KIMI_OUTPUT_PATH_CALL_IDS))('keeps the %s native identity unchanged under the OpenAI tool-call policy', (_scenario, id) => {
    expect(id).toMatch(/^[\w-]{1,64}$/)
  })

  it('gives each native feature case a distinct identity', () => {
    const ids = Object.values(KIMI_OUTPUT_PATH_CALL_IDS)
    expect(new Set(ids).size).toBe(ids.length)
  })
})

const pointer = `Tool output exceeded 50000 characters; the full output was saved to a file.
tool_name: Bash
tool_call_id: native-call
output_size_chars: 60000
output_size_bytes: 60000
output_path: /private/native/task-output.txt
next_step: Use Read with output_path to page through the saved output, or Grep to search it.

[preview: chars [0, 4096)]
head

[elided: chars [4096, 58976)]

[preview: chars [58976, 60000)]
tail`

const perLinePointer = `native preview[...truncated]
[Per-line truncation occurred; the complete output was saved to a file.
output_path: /private/native/task-output.txt
next_step: Use Read with output_path to page through the saved output, or Grep to search it.]

task_id: bash-abcd1234
output_size_bytes: 60000`

describe('kimiNativeOutputPointer', () => {
  it('reads the captured native wall-time envelope before the complete output pointer', () => {
    expect(kimiNativeOutputPointer(`Wall time: 0.117 seconds\n${pointer}`, 'native-call'))
      .toEqual({ path: '/private/native/task-output.txt', chars: 60000, bytes: 60000 })
  })

  it.each([
    { prefix: '<system>ERROR: Tool execution failed.</system>\n' },
    { prefix: 'Wall time: 0.000 seconds\n<system>ERROR: Tool execution failed.</system>\n' },
  ])('reads the native failed-output projection before a complete pointer: %j', ({ prefix }) => {
    expect(kimiNativeOutputPointer(`${prefix}${pointer}`, 'native-call'))
      .toEqual({ path: '/private/native/task-output.txt', chars: 60000, bytes: 60000 })
  })

  it('reads a complete per-line footer without inventing an absent character count', () => {
    expect(kimiNativeOutputPointer(`Wall time: 0.117 seconds\n${perLinePointer}`, 'native-call'))
      .toEqual({ path: '/private/native/task-output.txt' })
  })

  it('rejects a footer that saves only a prefix', () => {
    const partial = perLinePointer.replace('the complete output was saved', 'only the first 60000 characters (of 120000) were saved')
    expect(() => kimiNativeOutputPointer(partial, 'native-call')).toThrow('complete output-file pointer')
  })

  // The reader strips one well-formed wall time and one failure line. Text that then does not start with the complete
  // header must hold exactly one per-line footer, so each of these reaches the error of the footer reader.
  it.each([
    `Wall time: 0.117 seconds\nWall time: 0.117 seconds\n${pointer}`,
    `Wall time: -1.000 seconds\n${pointer}`,
    `Wall time: NaN seconds\n${pointer}`,
    `Wall time: 0.117 seconds\n<system>SUCCESS: Tool execution passed.</system>\n${pointer}`,
    `${perLinePointer}\n[Per-line truncation occurred; the complete output was saved to a file.\noutput_path: /private/other.txt\nnext_step: Use Read with output_path to page through the saved output, or Grep to search it.]`,
  ])('rejects malformed or ambiguous native envelopes and footers: %#', (text) => {
    expect(() => kimiNativeOutputPointer(text, 'native-call')).toThrow('The native Kimi result has no complete output-file pointer.')
  })
  it('reads the captured native model text-block projection', () => {
    const projected = JSON.stringify([{ type: 'text', text: pointer }])
    expect(kimiNativeOutputPointer(projected, 'native-call')).toEqual({ path: '/private/native/task-output.txt', chars: 60000, bytes: 60000 })
  })

  it('preserves the exact native reference and complete sizes', () => {
    expect(kimiNativeOutputPointer(pointer, 'native-call')).toEqual({ path: '/private/native/task-output.txt', chars: 60000, bytes: 60000 })
  })

  it('reads a generic MCP header only with the exact supplied native tool name', () => {
    const mcpName = 'mcp__echo_probe__echo'
    const mcpPointer = pointer.replace('tool_name: Bash', `tool_name: ${mcpName}`)
    expect(kimiNativeOutputPointer(mcpPointer, 'native-call', mcpName))
      .toEqual({ path: '/private/native/task-output.txt', chars: 60000, bytes: 60000 })
    expect(() => kimiNativeOutputPointer(mcpPointer, 'native-call')).toThrow('another tool or call')
    expect(() => kimiNativeOutputPointer(mcpPointer, 'native-call', 'mcp__other__echo')).toThrow('another tool or call')
    expect(() => kimiNativeOutputPointer(mcpPointer, 'native-call', '')).toThrow('complete output-file pointer')
  })

  it.each(['C:/private/native/task-output.txt', String.raw`C:\private\native\task-output.txt`, '//server/share/task-output.txt'])('reads a canonical native Windows file path on every test host: %j', (path) => {
    expect(kimiNativeOutputPointer(pointer.replace('/private/native/task-output.txt', path), 'native-call'))
      .toEqual({ path, chars: 60000, bytes: 60000 })
  })

  it.each([
    [pointer.replace('output_size_chars: 60000', 'output_size_chars: 060000'), 'has an invalid full path or complete character count'],
    [pointer.replace('next_step: Use Read with output_path to page through the saved output, or Grep to search it.', 'next_step: wrong'), 'requires its complete pointer header'],
    [pointer.replace('\nnext_step:', '\nunknown_field: printed\nnext_step:'), 'has an unknown pointer field'],
  ])('rejects a malformed complete header even when it contains a plausible path: %#', (text, error) => {
    expect(() => kimiNativeOutputPointer(text, 'native-call')).toThrow(error)
  })

  it('accepts the optional absent byte count', () => {
    expect(kimiNativeOutputPointer(pointer.replace('output_size_bytes: 60000\n', ''), 'native-call')).toEqual({ path: '/private/native/task-output.txt', chars: 60000 })
  })

  it.each(['-1', 'wrong', '', '9007199254740992'])('rejects a present malformed byte count: %j', (size) => {
    expect(() => kimiNativeOutputPointer(pointer.replace('output_size_bytes: 60000', `output_size_bytes: ${size}`), 'native-call')).toThrow('byte count')
  })

  it('keeps printed pointer-like lines outside the native reference header', () => {
    const output = `${pointer}\noutput_path: /private/printed-text.txt\ntool_call_id: printed-text\noutput_size_bytes: -1`
    expect(kimiNativeOutputPointer(output, 'native-call')).toEqual({ path: '/private/native/task-output.txt', chars: 60000, bytes: 60000 })
  })

  it.each(['tool_name: Bash', 'tool_call_id: native-call', 'output_size_chars: 60000', 'output_path: /private/native/task-output.txt'])('rejects the absent native field %s', (field) => {
    expect(() => kimiNativeOutputPointer(pointer.replace(`${field}\n`, ''), 'native-call')).toThrow('one exact')
  })

  it.each([
    ['output_size_chars: 60000', 'requires one exact output_size_chars field'],
    ['output_path: /private/native/task-output.txt', 'requires one exact output_path field'],
    ['output_size_bytes: 60000', 'repeats its byte count'],
  ])('rejects a repeated native field %s', (field, error) => {
    expect(() => kimiNativeOutputPointer(pointer.replace('\nnext_step:', `\n${field}\nnext_step:`), 'native-call')).toThrow(error)
  })

  it.each(['0', '-1', '50000', '10000001', 'NaN', '9007199254740992', '60000 (only the first 50000 characters were preserved)'])('rejects an incomplete or invalid character count %s', (size) => {
    expect(() => kimiNativeOutputPointer(pointer.replace('output_size_chars: 60000', `output_size_chars: ${size}`), 'native-call')).toThrow('character count')
  })

  it.each(['file:///private/native/task.txt', 'https://example.com/output', '/private/native/task\0.txt'])('rejects an invalid path %j', (path) => {
    expect(() => kimiNativeOutputPointer(pointer.replace('/private/native/task-output.txt', path), 'native-call')).toThrow('filesystem path')
  })

  it('rejects partial persistence and a different tool call', () => {
    expect(() => kimiNativeOutputPointer(pointer.replace('the full output was saved', 'only the first characters were saved'), 'native-call')).toThrow('complete output-file pointer')
    expect(() => kimiNativeOutputPointer(pointer, 'other-call')).toThrow('another tool or call')
    expect(() => kimiNativeOutputPointer(pointer.replace('tool_name: Bash', 'tool_name: Read'), 'native-call')).toThrow('another tool or call')
  })
})

describe('native filesystem path spelling', () => {
  it.each(['relative/task.txt', '/private/../native/task.txt'])('keeps a documented relative path or parent segment: %j', (path) => {
    expect(kimiNativeOutputPointer(pointer.replace('/private/native/task-output.txt', path), 'native-call').path).toBe(path)
  })
})
