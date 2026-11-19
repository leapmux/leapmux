import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

describe('openCodePlugin', () => {
  const plugin = providerFor(AgentProvider.OPENCODE)

  it('refuses audio that the native session converter discards', () => {
    expect(plugin?.configuration?.attachments?.audio).toBe(false)
  })

  it('registers native transcript and control capabilities', () => {
    expect(plugin?.transcript.classify(input({
      type: 'session.execution.succeeded',
      data: { sessionID: 'ses_1' },
    }))).toEqual({ kind: 'result_divider' })
    expect(plugin?.controls?.askUserQuestion).toBeDefined()
    expect(plugin?.controls?.elicitation).toBeDefined()
    expect(plugin?.configuration?.planMode?.groupKey).toBe('primaryAgent')
    expect(plugin?.configuration?.supportsSubagentSend).toBe(true)
  })

  it('resolves native supplemental tool records before classification', () => {
    expect(plugin?.transcript.resolveMessage).toBeDefined()
    expect(plugin?.transcript.classify(input({
      type: 'session.tool.called',
      data: {
        sessionID: 'ses_1',
        assistantMessageID: 'msg_1',
        id: 'c',
        name: 'shell',
        input: { command: 'pwd' },
      },
    }))).toEqual({ kind: 'tool_use' })
  })

  it('reads a native opening without requesting another row', () => {
    expect((plugin?.transcript.needsRequest?.(input({
      type: 'session.tool.called',
      data: {
        sessionID: 'ses_1',
        assistantMessageID: 'msg_1',
        id: 'c',
        name: 'shell',
        input: { command: 'pwd' },
      },
    }))) ?? false).toBe(false)
    expect(plugin?.transcript.needsRequest?.(input({ type: 'unknown' })) ?? false).toBe(false)
  })
})
