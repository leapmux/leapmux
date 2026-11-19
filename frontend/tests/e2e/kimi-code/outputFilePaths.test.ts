import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import spans from '../../../../testdata/kimi_conversation_span_conformance.json'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { KIMI_OUTPUT_PATH_CALL_IDS, kimiNativeOutputPointer, kimiNativeToolSpan, readKimiNativeOutput } from './outputFilePaths'

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

function currentKimiOutput() {
  const identity = spans.cases[0]!
  const output = pointer.replace('/private/native/task-output.txt', '/private/native-session/main/task-output.txt')
  const frame = {
    type: 'leapmux.kimi.item',
    session_id: identity.session,
    agent_id: identity.agent,
    turn_id: identity.turn,
    role: 'result',
    item: {
      type: 'tool_call',
      item_id: identity.item,
      status: 'ended',
      outcome: 'completed',
      created_at: '2026-11-18T00:00:00Z',
      started_at: '2026-11-18T00:00:00Z',
      name: 'Bash',
      input: { command: 'native command' },
      output,
    },
  }
  const message = create(AgentChatMessageSchema, {
    id: 'worker-row',
    seq: 1n,
    agentSessionId: identity.session,
    spanId: identity.span,
    content: new TextEncoder().encode(JSON.stringify(frame)),
    contentCompression: ContentCompression.NONE,
  })
  return {
    frame,
    message,
    snapshot: {
      agentId: 'worker-agent',
      agentSessionId: identity.session,
      messages: [message],
    },
  }
}

describe('readKimiNativeOutput', () => {
  it('reads one current native result under its exact request span', () => {
    const { snapshot, frame } = currentKimiOutput()
    const receipt = readKimiNativeOutput(snapshot, 'native-call', 'Bash')
    expect(receipt.path).toBe('/private/native-session/main/task-output.txt')
    expect(receipt.chars).toBe(60000)
    expect(receipt.bytes).toBe(60000)
    expect(receipt.previewText).toBe(frame.item.output)
    expect(receipt.frame).toStrictEqual(frame)
  })

  it.each([
    'session',
    'agent',
    'turn',
    'item',
    'role',
    'tool',
    'running',
    'span',
  ])('refuses another %s owner', (field) => {
    const { snapshot, frame, message } = currentKimiOutput()
    switch (field) {
      case 'session':
        frame.session_id = 'foreign'
        break
      case 'agent':
        frame.agent_id = 'agent-0'
        break
      case 'turn':
        frame.turn_id = 'foreign'
        break
      case 'item':
        frame.item.item_id = 'foreign'
        break
      case 'role':
        frame.role = 'request'
        break
      case 'tool':
        frame.item.name = 'foreign'
        break
      case 'running':
        frame.item.status = 'running'
        break
      case 'span':
        message.spanId = 'foreign'
        break
    }
    message.content = new TextEncoder().encode(JSON.stringify(frame))
    expect(() => readKimiNativeOutput(snapshot, 'native-call', 'Bash')).toThrow('exactly one accepted record')
  })

  it('refuses two accepted records even when both have the expected native identity', () => {
    const { snapshot, message } = currentKimiOutput()
    snapshot.messages.push(create(AgentChatMessageSchema, { ...message, id: 'another-row', seq: 2n }))
    expect(() => readKimiNativeOutput(snapshot, 'native-call', 'Bash')).toThrow('exactly one accepted record')
  })

  it('reads failed native output from error without substituting the success field', () => {
    const { snapshot, frame, message } = currentKimiOutput()
    Object.assign(frame.item, { outcome: 'failed', error: frame.item.output, output: undefined })
    message.content = new TextEncoder().encode(JSON.stringify(frame))
    expect(readKimiNativeOutput(snapshot, 'native-call', 'Bash').path).toBe('/private/native-session/main/task-output.txt')
  })

  it('refuses a complete pointer for another native path owner', () => {
    const { snapshot, frame, message } = currentKimiOutput()
    frame.item.output = frame.item.output.replace('/native-session/main/', '/foreign/main/')
    message.content = new TextEncoder().encode(JSON.stringify(frame))
    expect(() => readKimiNativeOutput(snapshot, 'native-call', 'Bash')).toThrow('another session or agent')
  })
})

describe('kimiNativeToolSpan', () => {
  it.each(spans.cases)('matches the shared Worker vector $name', (test) => {
    expect(kimiNativeToolSpan(test.session, test.agent, test.turn, test.item)).toBe(test.span)
  })

  it.each([0, 1, 2, 3])('refuses missing native identity component %s', (missing) => {
    const owner: [string, string, string, string] = ['native', 'main', 'turn', 'item']
    owner[missing] = ''
    expect(() => kimiNativeToolSpan(...owner)).toThrow('complete session, agent, turn and item')
  })
})

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
    const projected = JSON.stringify([
      { type: 'text', text: pointer },
    ])
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
    [
      pointer.replace('output_size_chars: 60000', 'output_size_chars: 060000'),
      'has an invalid full path or complete character count',
    ],
    [
      pointer.replace('next_step: Use Read with output_path to page through the saved output, or Grep to search it.', 'next_step: wrong'),
      'requires its complete pointer header',
    ],
    [
      pointer.replace('\nnext_step:', '\nunknown_field: printed\nnext_step:'),
      'has an unknown pointer field',
    ],
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

  it.each([
    'tool_name: Bash',
    'tool_call_id: native-call',
    'output_size_chars: 60000',
    'output_path: /private/native/task-output.txt',
  ])('rejects the absent native field %s', (field) => {
    expect(() => kimiNativeOutputPointer(pointer.replace(`${field}\n`, ''), 'native-call')).toThrow('one exact')
  })

  it.each([
    ['output_size_chars: 60000', 'requires one exact output_size_chars field'],
    ['output_path: /private/native/task-output.txt', 'requires one exact output_path field'],
    ['output_size_bytes: 60000', 'repeats its byte count'],
  ])('rejects a repeated native field %s', (field, error) => {
    expect(() => kimiNativeOutputPointer(pointer.replace('\nnext_step:', `\n${field}\nnext_step:`), 'native-call')).toThrow(error)
  })

  it.each([
    '0',
    '-1',
    '50000',
    '10000001',
    'NaN',
    '9007199254740992',
    '60000 (only the first 50000 characters were preserved)',
  ])('rejects an incomplete or invalid character count %s', (size) => {
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
