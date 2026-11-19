import type { ChatRow } from './model/row'
import { describe, expect, it } from 'vitest'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { agentMessageSenderOf, withAgentMessageSender } from './leapmuxRows'

const FIELD = MESSAGE_METADATA_FIELD.AgentMessageSender

describe('agentMessageSenderOf', () => {
  it('reads the sender that the Worker writes in its protobuf JSON form', () => {
    expect(agentMessageSenderOf({
      [FIELD]: { tabId: 'sender-tab', provider: 'AGENT_PROVIDER_CODEX' },
    }))
      .toEqual({ tabId: 'sender-tab', provider: AgentProvider.CODEX })
  })

  it('reads a provider that the JSON states as its number', () => {
    expect(agentMessageSenderOf({
      [FIELD]: { tabId: 'sender-tab', provider: AgentProvider.CLAUDE_CODE },
    }))
      .toEqual({ tabId: 'sender-tab', provider: AgentProvider.CLAUDE_CODE })
  })

  it('ignores a field that a later Worker adds', () => {
    expect(agentMessageSenderOf({
      [FIELD]: { tabId: 'sender-tab', provider: 'AGENT_PROVIDER_CODEX', title: 'later' },
    }))
      .toEqual({ tabId: 'sender-tab', provider: AgentProvider.CODEX })
  })

  it('reads no sender from metadata that states none', () => {
    expect(agentMessageSenderOf(undefined)).toBeNull()
    expect(agentMessageSenderOf(null)).toBeNull()
    expect(agentMessageSenderOf('metadata')).toBeNull()
    expect(agentMessageSenderOf({})).toBeNull()
    expect(agentMessageSenderOf({ duration_ms: 7 })).toBeNull()
  })

  it('reads no sender from a value that does not decode', () => {
    expect(agentMessageSenderOf({ [FIELD]: 'sender-tab' })).toBeNull()
    expect(agentMessageSenderOf({
      [FIELD]: { tabId: 7 },
    })).toBeNull()
  })

  // A later Worker may know a provider that this browser does not. The message
  // still came from another agent, so the row keeps its sender, and the icon and
  // the name fall back to the unknown provider's.
  it('keeps the sender of a provider that this browser does not know', () => {
    expect(agentMessageSenderOf({
      [FIELD]: { tabId: 'sender-tab', provider: 'AGENT_PROVIDER_FROM_THE_FUTURE' },
    }))
      .toEqual({ tabId: 'sender-tab', provider: AgentProvider.UNSPECIFIED })
  })

  it('reads no sender from a sender that states no tab', () => {
    expect(agentMessageSenderOf({
      [FIELD]: { provider: 'AGENT_PROVIDER_CODEX' },
    })).toBeNull()
    expect(agentMessageSenderOf({
      [FIELD]: { tabId: '', provider: 'AGENT_PROVIDER_CODEX' },
    })).toBeNull()
  })
})

describe('withAgentMessageSender', () => {
  const metadata = {
    [FIELD]: { tabId: 'sender-tab', provider: 'AGENT_PROVIDER_CODEX' },
  }
  const user: ChatRow = { kind: 'user', text: 'Run the tests', attachments: [] }

  it('adds the sender to a message', () => {
    expect(withAgentMessageSender(user, metadata))
      .toEqual({
        ...user,
        sender: { tabId: 'sender-tab', provider: AgentProvider.CODEX },
      })
  })

  it('returns the same row when the metadata states no sender', () => {
    expect(withAgentMessageSender(user, {})).toBe(user)
  })

  it('leaves a row that is not a message as it is', () => {
    const plan: ChatRow = { kind: 'plan-execution', text: 'Implement the plan.' }
    expect(withAgentMessageSender(plan, metadata)).toBe(plan)
  })
})
