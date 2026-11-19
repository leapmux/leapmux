import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { agentMessageSenderLabel } from './agentMessageSender'

describe('agentMessageSenderLabel', () => {
  const sender = { tabId: 'sender-tab', provider: AgentProvider.CLAUDE_CODE }

  it('states the title of the sending tab and its provider', () => {
    expect(agentMessageSenderLabel(sender, id => (id === 'sender-tab'
      ? 'Fix flaky spec'
      : undefined)))
      .toBe('Message from Fix flaky spec (Claude Code)')
  })

  it('states the tab id when the browser holds no title for the tab', () => {
    expect(agentMessageSenderLabel(sender, () => undefined)).toBe('Message from sender-tab (Claude Code)')
    expect(agentMessageSenderLabel(sender, () => '   ')).toBe('Message from sender-tab (Claude Code)')
  })

  it('states the tab id when no lookup is at hand', () => {
    expect(agentMessageSenderLabel(sender, undefined)).toBe('Message from sender-tab (Claude Code)')
  })

  it('trims the white space around a title', () => {
    expect(agentMessageSenderLabel(sender, () => '  Fix flaky spec\n')).toBe('Message from Fix flaky spec (Claude Code)')
  })

  // agentMessageSenderOf reads a provider that this browser does not know as
  // UNSPECIFIED, and the row keeps its sender.
  it('states an unknown provider for UNSPECIFIED', () => {
    expect(agentMessageSenderLabel({ tabId: 'sender-tab', provider: AgentProvider.UNSPECIFIED }, () => 'Fix flaky spec'))
      .toBe('Message from Fix flaky spec (Unknown)')
  })
})
