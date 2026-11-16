import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { openCodeToolCall } from './toolCall'
import { openCodeToolRecord } from './toolCommon'

function completed(name: string, input: Record<string, unknown>, text: string, metadata: Record<string, unknown> = {}) {
  const record = openCodeToolRecord({
    type: 'tool',
    id: 'call_1',
    name,
    state: {
      status: 'completed',
      input,
      metadata,
      content: [
        { type: 'text', text },
      ],
    },
  })
  if (!record)
    throw new Error('The native completed fixture must decode.')
  return openCodeToolCall({
    record,
    spanId: 'worker-scoped-call',
    rowFinal: true,
    completion: MessageCompletion.COMPLETE,
  })
}

describe('openCodeToolCall', () => {
  it('retains images returned by the native Code Mode executor', () => {
    const record = openCodeToolRecord({
      type: 'tool',
      id: 'image-call',
      name: 'execute',
      state: {
        status: 'completed',
        input: { code: 'return await tools.server.image({})' },
        content: [
          { type: 'file', uri: 'data:image/png;base64,AQID', mime: 'image/png' },
        ],
      },
    })
    if (!record)
      throw new Error('The native Code Mode image fixture must decode.')
    expect(openCodeToolCall({ record, spanId: 'worker-image-span', rowFinal: true }).images)
      .toEqual([
        { mimeType: 'image/png', data: 'AQID' },
      ])
  })

  it('marks a Code Mode script failure from its native metadata', () => {
    expect(completed('execute', { code: 'throw new Error("failed")' }, 'script failure', { error: true })).toMatchObject({
      status: 'failed',
      result: {
        commands: [
          { output: 'script failure', failed: true },
        ],
      },
    })
    expect(completed('execute', { code: 'return "error"' }, 'error', { error: false })).toMatchObject({ status: 'completed' })
  })

  it('reads native shell commands and zero exit codes', () => {
    expect(completed('shell', { command: 'pwd', workdir: '/work' }, '/work', { exit: 0 })).toMatchObject({
      kind: 'execute',
      status: 'completed',
      request: { command: 'pwd', language: 'bash', cwd: '/work' },
      result: {
        commands: [
          { output: '/work', exitCode: 0 },
        ],
      },
    })
  })

  it('reads JavaScript code without treating it as a shell command', () => {
    expect(completed('execute', { code: 'return 0' }, '0')).toMatchObject({
      kind: 'execute',
      request: { command: 'return 0', language: 'javascript' },
    })
  })

  it('reads native numbered file pages', () => {
    expect(completed('read', { path: '/file', offset: 3, limit: 1 }, 'Read file /file, lines 3-3\n3: selected')).toMatchObject({
      kind: 'read',
      request: { path: '/file', offset: 3, limit: 1 },
      result: {
        lines: [
          { num: 3, text: 'selected' },
        ],
      },
    })
  })

  it('uses the native edit patch for landed changes', () => {
    const call = completed('edit', { path: '/file', oldString: 'old', newString: 'new' }, 'Edited file', {
      files: [
        { file: '/file', patch: '--- /file\n+++ /file\n@@ -1 +1 @@\n-old\n+new\n' },
      ],
    })
    expect(call).toMatchObject({
      kind: 'edit',
      result: {
        changes: [
          {
            filePath: '/file',
            structuredPatch: [
              {
                oldStart: 1,
                newStart: 1,
                lines: ['-old', '+new'],
              },
            ],
          },
        ],
      },
    })
  })

  it('recognizes native empty searches without inventing matches', () => {
    expect(completed('grep', { pattern: 'absent' }, 'No matches found', { matches: 0 })).toMatchObject({
      kind: 'grep',
      result: { empty: true, matchCount: 0, lines: [] },
    })
    expect(completed('glob', { pattern: '*.absent' }, 'No files found', { count: 0 })).toMatchObject({
      kind: 'glob',
      result: { empty: true, filenames: [] },
    })
  })

  it('keeps custom tool content and arguments', () => {
    expect(completed('custom_tool', { count: 0, enabled: false }, 'custom output')).toMatchObject({
      kind: 'mcp',
      request: {
        args: { count: 0, enabled: false },
        tool: 'custom_tool',
      },
      result: {
        content: [
          { type: 'text', text: 'custom output' },
        ],
      },
    })
  })

  it('reads an explicit native user rejection as declined', () => {
    const record = openCodeToolRecord({
      type: 'tool',
      id: 'c',
      name: 'shell',
      state: {
        status: 'error',
        input: { command: 'denied' },
        error: { type: 'aborted', message: 'The user declined this tool call' },
      },
    })
    if (!record)
      throw new Error('The native rejected fixture must decode.')
    expect(openCodeToolCall({
      record,
      spanId: 'worker-scoped-call',
      rowFinal: true,
      completion: MessageCompletion.ERROR,
    })).toMatchObject({
      kind: 'execute',
      status: 'declined',
      result: { failure: true, text: 'The user declined this tool call' },
    })
  })
})
