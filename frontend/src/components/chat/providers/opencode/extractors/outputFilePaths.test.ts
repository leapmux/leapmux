import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall } from '~/test-support/toolCallFixture'
import '../plugin'

const frame = {
  type: 'session.tool.success',
  data: {
    sessionID: 'ses_1',
    assistantMessageID: 'msg_1',
    id: 'call_1',
    name: 'shell',
    input: { command: 'printf preview' },
    content: [
      { type: 'text', text: 'preview' },
    ],
    metadata: { truncated: true, outputPath: '/native/data/shell/shl_1/output' },
  },
}
const options = { spanId: 'worker-span', agentSessionId: 'ses_1' }

describe('openCodeOutputFilePaths', () => {
  it('reads the native shell footer when v2 omits the structured pointer', () => {
    const native = {
      ...frame,
      data: {
        ...frame.data,
        content: [
          { type: 'text', text: 'tail\n\n[showing lines 10-20 of 20; full output saved to /native/shell/sh_1.out]' },
        ],
        metadata: { truncated: true, shellID: 'sh_1' },
      },
    }
    expect(providerToolCall(AgentProvider.OPENCODE, native, options)?.outputFilePaths).toEqual(['/native/shell/sh_1.out'])
  })

  it.each([
    { truncated: false, shellID: 'sh_1' },
    { truncated: true, shellID: 'other' },
    { truncated: true },
  ])('rejects an unverified footer for %j', (metadata) => {
    const native = {
      ...frame,
      data: {
        ...frame.data,
        content: [
          { type: 'text', text: 'tail\n\n[showing lines 10-20 of 20; full output saved to /native/shell/sh_1.out]' },
        ],
        metadata,
      },
    }
    expect(providerToolCall(AgentProvider.OPENCODE, native, options)?.outputFilePaths).toBeUndefined()
  })

  it('attaches the native pointer without reading or changing the inline result', () => {
    const before = JSON.stringify(frame)
    const call = providerToolCall(AgentProvider.OPENCODE, frame, options)
    expect(call?.outputFilePaths).toEqual(['/native/data/shell/shl_1/output'])
    expect(JSON.stringify(frame)).toBe(before)
  })

  it.each(['request', 'none', 'other'] as const)('omits paths from the %s side', (role) => {
    expect(outputFilePathFixture(AgentProvider.OPENCODE, frame, options)(frame, { role })).toEqual([])
  })

  it.each([
    'https://example.com/output',
    'file:///output',
    'relative/output',
    ' ',
    '/bad\0path',
  ])('rejects a non-filesystem pointer %j', (outputPath) => {
    const read = outputFilePathFixture(AgentProvider.OPENCODE, frame, options)
    expect(read({
      ...frame,
      data: {
        ...frame.data,
        metadata: { outputPath },
      },
    })).toEqual([])
  })
})
