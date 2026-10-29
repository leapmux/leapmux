import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall, providerToolMeta } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/goose/.tmp123/stdout-0'
const notice = (path: string) => `[Output exceeded 2000 line limit (6001 lines total). Full output saved to ${path}. Read it with shell commands like \`head\`, \`tail\`, or \`sed -n '100,200p'\` up to 2000 lines at a time.]`
const frame = {
  sessionUpdate: 'tool_call_update',
  toolCallId: 'native-call',
  status: 'completed',
  rawInput: { command: 'printf native-preview' },
  _meta: {
    goose: {
      toolCall: { toolName: 'shell', extensionName: 'developer' },
    },
  },
  content: [
    {
      type: 'content',
      content: { type: 'text', text: 'native inline preview' },
    },
    {
      type: 'content',
      content: {
        type: 'text',
        text: notice(path),
      },
    },
  ],
}
const options = { spanId: 'native-call', spanType: 'tool_call', agentSessionId: 'native-session' }
const pathsFor = () => outputFilePathFixture(AgentProvider.GOOSE, frame, options)

describe('registered Goose output file paths', () => {
  it('attaches the stated native paths without changing the original frame', () => {
    const before = JSON.stringify(frame)
    expect(providerToolCall(AgentProvider.GOOSE, frame, options)?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(frame)).toBe(before)
  })

  it('keeps distinct native paths in order and removes duplicates', () => {
    const other = '/native/goose/.tmp123/output-0'
    const payload = {
      ...frame,
      content: [
        {
          type: 'content',
          content: {
            type: 'text',
            text: [
              notice(path),
              notice(other),
              notice(path),
            ].join('\n'),
          },
        },
      ],
    }
    expect(pathsFor()(payload)).toEqual([path, other])
  })

  it.each(['completed', 'failed'])('keeps pointers for a %s call', (status) => {
    expect(pathsFor()({ ...frame, status })).toEqual([path])
  })

  it.each([null, false, 0, -1, '', [], {}].map(payload => [payload]))('refuses an invalid native payload: %j', (payload) => {
    expect(pathsFor()(payload)).toEqual([])
  })

  it.each(['request', 'none', 'other'] as const)('refuses the %s role', (role) => {
    expect(pathsFor()(frame, { role })).toEqual([])
  })

  it.each(['pending', 'in_progress', 'cancelled'])('refuses an unfinished or interrupted native result: %s', (status) => {
    expect(pathsFor()({ ...frame, status })).toEqual([])
  })

  it('reads prefixed native tool names', () => {
    expect(pathsFor()({
      ...frame,
      _meta: {
        goose: {
          toolCall: { toolName: 'developer__shell' },
        },
      },
    })).toEqual([path])
  })

  it('refuses another call and another tool', () => {
    expect(pathsFor()({ ...frame, toolCallId: 'foreign' })).toEqual([])
    expect(pathsFor()({
      ...frame,
      _meta: {
        goose: {
          toolCall: { toolName: 'read', extensionName: 'developer' },
        },
      },
    })).toEqual([])
    expect(pathsFor()({
      ...frame,
      _meta: {
        goose: {
          toolCall: { toolName: 'shell', extensionName: 'other' },
        },
      },
    })).toEqual([])
  })

  it.each([
    'https://example.com/stdout-0',
    'file:///native/stdout-0',
    'relative/stdout-0',
    '/native/zero\0byte/stdout-0',
    '/native/stdout-0\r',
  ])('refuses a non-filesystem pointer: %j', (invalid) => {
    expect(pathsFor()(JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1))))).toEqual([])
  })

  it.each(['/native/space here/stdout-0', 'C:\\native\\output-3'])('preserves a native filesystem path: %s', (valid) => {
    expect(pathsFor()(JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(valid).slice(1, -1))))).toEqual([valid])
  })

  it('does not derive a pointer from incomplete output or a bare filename', () => {
    for (const text of [
      path,
      `Full output saved to ${path}`,
      notice(path).slice(0, -1),
      `prefix ${notice(path)}`,
    ]) {
      expect(pathsFor()({
        ...frame,
        content: [
          {
            type: 'content',
            content: { type: 'text', text },
          },
        ],
      })).toEqual([])
    }
  })

  it('preserves the native preview when an unrelated supplement supplies full text', () => {
    const meta = providerToolMeta(AgentProvider.GOOSE, frame, {
      ...options,
      supplementalContent: {
        completeOutput: { path, text: 'FORGED_BODY' },
      },
    })
    expect(meta?.copyableContent()).toContain('native inline preview')
    expect(meta?.copyableContent()).not.toContain('FORGED_BODY')
  })
})
