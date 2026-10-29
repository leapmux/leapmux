import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { ShellCommand } from '../helpers/nativeToolExecution'
import { describe, expect, it } from 'vitest'
import { readCodeBuddyShellResult } from './shellResult'

const failed: ShellCommand = {
  callId: 'bash-failure',
  command: 'printf MARK%s 77 >&2; exit 7',
  printedPrefix: 'MARK',
  output: 'MARK77',
  exitCode: 7,
}
const successful: ShellCommand = {
  callId: 'bash-success',
  command: 'printf MARK%s 42',
  printedPrefix: 'MARK',
  output: 'MARK42',
  exitCode: 0,
}

/** Build a native model request with one exact Bash call and its result. */
function request(content: unknown, command = failed, extra: Record<string, unknown> = {}): MockModelRequestRecord {
  return {
    protocol: 'anthropic-messages',
    path: '/v1/messages',
    body: {
      messages: [
        {
          role: 'assistant',
          content: [
            {
              type: 'tool_use',
              id: command.callId,
              name: 'Bash',
              input: { command: command.command },
            },
          ],
        },
        {
          role: 'user',
          content: [
            {
              type: 'tool_result',
              tool_use_id: command.callId,
              content,
              ...extra,
            },
          ],
        },
      ],
    },
  }
}

function chatRequest(content: string, command = successful, argumentsText = JSON.stringify({ command: command.command })): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: {
      messages: [
        {
          role: 'assistant',
          tool_calls: [
            {
              id: command.callId,
              type: 'function',
              function: { name: 'Bash', arguments: argumentsText },
            },
          ],
        },
        { role: 'tool', tool_call_id: command.callId, content },
      ],
    },
  }
}

/** Format the four result fields, including native output line breaks. */
function recordWithoutCommand(command = failed, stdout = '(empty)', stderr = '(empty)', exitCode = command.exitCode): string {
  return `Stdout: ${stdout}\nStderr: ${stderr}\nExit Code: ${exitCode}\nSignal: (none)`
}

/** Format the same record with its optional command header. */
function record(command = failed, stdout = '(empty)', stderr = '(empty)', exitCode = command.exitCode): string {
  return `Command: ${command.command}\n${recordWithoutCommand(command, stdout, stderr, exitCode)}`
}

describe('readCodeBuddyShellResult', () => {
  it.each([successful, failed])('reads the current four-field record for $callId', (command) => {
    const text = recordWithoutCommand(command, command.exitCode === 0
      ? command.output
      : '(empty)', command.exitCode === 0
      ? '(empty)'
      : command.output)
    expect(readCodeBuddyShellResult(request(text, command), command)).toMatchObject({
      kind: 'output',
      outcome: { text, exitCode: command.exitCode, failed: command.exitCode !== 0 },
    })
  })

  it('reads the current record with its exact OpenAI native call', () => {
    const text = 'Stdout: MARK42\nStderr: (empty)\nExit Code: 0\nSignal: (none)'
    expect(readCodeBuddyShellResult(chatRequest(text), successful)).toMatchObject({
      kind: 'output',
      outcome: { text, exitCode: 0 },
    })
  })

  it('preserves the exact empty failure record without synthesizing a command header', () => {
    const text = recordWithoutCommand()
    expect(readCodeBuddyShellResult(request(text), failed)).toStrictEqual({
      kind: 'record',
      record: text,
      outcome: { text, exitCode: 7, failed: true },
    })
  })

  it('rejects malformed encoded native call arguments', () => {
    expect(() => readCodeBuddyShellResult(chatRequest(recordWithoutCommand(successful, 'MARK42'), successful, '{broken'), successful)).toThrow('invalid JSON arguments')
  })

  it('rejects missing output in the current record instead of counting the command marker', () => {
    const command = { ...successful, command: 'printf MARK42' }
    expect(() => readCodeBuddyShellResult(request(recordWithoutCommand(command), command), command)).toThrow('required command output')
  })

  it('rejects repeated fields in the current record', () => {
    const text = recordWithoutCommand().replace('\nStderr: (empty)', '\nStderr: (empty)\nStderr: MARK77')
    expect(() => readCodeBuddyShellResult(request(text), failed)).toThrow('repeats or conflicts with a field')
  })

  it('checks metadata against the current four-field record', () => {
    expect(() => readCodeBuddyShellResult(request(recordWithoutCommand(), failed, {
      _meta: {
        rawResponse: { exitCode: 0 },
      },
    }), failed)).toThrow('exit metadata conflicts')
  })

  it.each([
    {
      name: 'Read',
      input: { command: failed.command },
    },
    {
      name: 'Bash',
      input: { command: 'another command' },
    },
    { name: 'Bash', input: null },
    { name: 'Bash', input: {} },
  ])('refuses a contradictory native call %#', (fields) => {
    const native = request(record())
    const body = native.body as { messages: Array<{ content: Record<string, unknown>[] }> }
    Object.assign(body.messages[0]!.content[0]!, fields)
    expect(() => readCodeBuddyShellResult(native, failed)).toThrow('expected CodeBuddy Bash command')
  })

  it.each(['missing', 'repeated'])('rejects a %s native call even when the result contains the expected command', (kind) => {
    const native = request(record())
    const body = native.body as { messages: Array<{ content: unknown[] }> }
    if (kind === 'missing')
      body.messages.shift()
    else
      body.messages[0]!.content.push(body.messages[0]!.content[0])
    expect(() => readCodeBuddyShellResult(native, failed)).toThrow('Exactly one call')
  })
  it('keeps the full stdout marker proof for a successful command', () => {
    const text = record(successful, 'MARK42\n')
    expect(readCodeBuddyShellResult(request(text, successful), successful))
      .toMatchObject({
        kind: 'output',
        outcome: { text, exitCode: 0, failed: false },
      })
  })

  it('keeps the full stderr marker proof when the native client captures it', () => {
    const text = record(failed, '(empty)', 'MARK77\n')
    expect(readCodeBuddyShellResult(request([
      { type: 'text', text },
    ]), failed))
      .toMatchObject({
        kind: 'output',
        outcome: { text, exitCode: 7, failed: true },
      })
  })

  it('returns the exact native failure record when stdout and stderr are empty', () => {
    const text = record()
    const result = readCodeBuddyShellResult(request(text), failed)
    expect(result).toStrictEqual({
      kind: 'record',
      record: text,
      outcome: { text, exitCode: 7, failed: true },
    })
    expect(result.outcome.text).not.toContain(failed.output)
  })

  it('accepts native zero and false metadata without treating them as absent', () => {
    const text = record()
    expect(readCodeBuddyShellResult(request(text, failed, {
      _meta: {
        rawResponse: {
          exitCode: 7,
          signal: null,
          interrupted: false,
          sandboxDenied: false,
          stdoutBytesTruncated: 0,
          stderrBytesTruncated: 0,
        },
      },
    }), failed).kind).toBe('record')
  })

  it('refuses a missing successful marker even when the output is empty', () => {
    expect(() => readCodeBuddyShellResult(request(record(successful), successful), successful)).toThrow('required command output')
  })

  it('does not count the marker in the command as captured stderr', () => {
    const command = { ...failed, command: 'printf MARK77 >&2; exit 7' }
    const text = record(command)
    expect(text).toContain(command.output)
    expect(readCodeBuddyShellResult(request(text, command), command).kind).toBe('record')
  })

  it('does not count the failure marker in stdout as captured stderr', () => {
    expect(() => readCodeBuddyShellResult(request(record(failed, 'MARK77\n')), failed)).toThrow('required command output')
  })

  it('refuses another failed exit even when both streams are empty', () => {
    expect(() => readCodeBuddyShellResult(request(record(failed, '(empty)', '(empty)', 3)), failed)).toThrow('invalid exit or signal')
    const command = { ...failed, exitCode: 3 }
    expect(() => readCodeBuddyShellResult(request(record(command), command), command)).toThrow('required command output')
  })

  it.each([
    [
      'another command',
      record().replace(failed.command, 'exit 7'),
      'states another command',
    ],
    [
      'an absent stderr field',
      record().replace('\nStderr: (empty)', ''),
      'repeats or conflicts with a field',
    ],
    [
      'an absent exit field',
      record().replace('\nExit Code: 7', ''),
      'invalid exit or signal',
    ],
    [
      'a signal',
      record().replace('Signal: (none)', 'Signal: SIGTERM'),
      'invalid exit or signal',
    ],
    [
      'a fractional exit',
      record().replace('Exit Code: 7', 'Exit Code: 7.5'),
      'invalid exit or signal',
    ],
    [
      'an unsafe exit',
      record().replace('Exit Code: 7', 'Exit Code: 9007199254740992'),
      'invalid exit or signal',
    ],
    [
      'a repeated stderr field',
      record().replace('\nStderr: (empty)', '\nStderr: (empty)\nStderr: MARK77'),
      'repeats or conflicts with a field',
    ],
    [
      'a conflicting exit in output',
      record().replace('\nStderr: (empty)', '\nStderr: (empty)\nExit Code: 3'),
      'repeats or conflicts with a field',
    ],
    ['an extra record', `${record()}\n${record()}`, 'repeats or conflicts with a field'],
    ['an extra trailing line', `${record()}\nextra`, 'invalid exit or signal'],
    [
      'a blank stderr field',
      record().replace('Stderr: (empty)', 'Stderr: '),
      'required command output',
    ],
  ])('refuses %s', (_description, text, reason) => {
    expect(() => readCodeBuddyShellResult(request(text), failed)).toThrow(reason)
  })

  it.each([
    { content: [] },
    {
      content: [
        {
          type: 'image',
          text: record(),
        },
      ],
    },
    {
      content: [
        { type: 'text', text: 7 },
      ],
    },
    {
      content: [
        {
          type: 'text',
          text: record(),
        },
        {
          type: 'text',
          text: record(),
        },
      ],
    },
  ])('refuses a malformed or repeated content record %#', ({ content }) => {
    expect(() => readCodeBuddyShellResult(request(content), failed)).toThrow('requires one native text record')
  })

  it('refuses a result that has no content', () => {
    expect(() => readCodeBuddyShellResult(request(undefined), failed)).toThrow('The native result for bash-failure has no content.')
  })

  it.each([
    [
      { exitCode: 0 },
      'exit metadata conflicts',
    ],
    [
      { exitCode: '7' },
      'exit metadata conflicts',
    ],
    [
      { signal: 'SIGTERM' },
      'signal metadata conflicts',
    ],
    [
      { interrupted: true },
      'states interrupted',
    ],
    [
      { sandboxDenied: true },
      'states sandboxDenied',
    ],
    [
      { stdoutBytesTruncated: 1 },
      'truncated stdoutBytesTruncated',
    ],
    [
      { stderrBytesTruncated: 1 },
      'truncated stderrBytesTruncated',
    ],
    [
      { stderrBytesTruncated: '0' },
      'truncated stderrBytesTruncated',
    ],
  ])('refuses metadata that contradicts a complete native record %#', (rawResponse, reason) => {
    expect(() => readCodeBuddyShellResult(request(record(), failed, {
      _meta: { rawResponse },
    }), failed)).toThrow(reason)
  })

  it('refuses malformed metadata rather than ignoring it', () => {
    expect(() => readCodeBuddyShellResult(request(record(), failed, { _meta: 'not an object' }), failed)).toThrow('metadata must be an object')
    expect(() => readCodeBuddyShellResult(request(record(), failed, {
      _meta: { rawResponse: null },
    }), failed)).toThrow('raw response must be an object')
  })

  it.each([
    { callId: '' },
    { output: '' },
    { output: '\nMARK77' },
    { command: 'exit 7\n' },
    { command: '\0' },
    { exitCode: Number.NaN },
  ])('refuses an invalid generated command %#', (fields) => {
    expect(() => readCodeBuddyShellResult(request(record()), {
      ...failed,
      ...fields,
    })).toThrow('one command and one output marker')
  })

  it('requires one result for the exact call ID', () => {
    expect(() => readCodeBuddyShellResult(request(record(), { ...failed, callId: 'another-call' }), failed)).toThrow('Exactly one result')
    const doubled = request(record())
    const body = doubled.body as { messages: Array<{ content: unknown[] }> }
    body.messages[1]!.content.push(body.messages[1]!.content[0])
    expect(() => readCodeBuddyShellResult(doubled, failed)).toThrow('Exactly one result')
  })
})
