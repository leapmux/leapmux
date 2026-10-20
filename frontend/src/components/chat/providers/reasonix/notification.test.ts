import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import { reasonixNotificationEntry } from './notification'
import './plugin'

const warning = {
  sessionUpdate: 'agent_message_chunk',
  content: { type: 'text', text: '\n\n[warning] Guidance was not applied because the turn ended before it could be processed.' },
}

const compacted = {
  sessionUpdate: 'agent_message_chunk',
  content: { type: 'text', text: '\n\n[compacted 42 earlier messages to save context]' },
}

describe('reasonixNotificationEntry', () => {
  const plugin = providerFor(AgentProvider.REASONIX)!

  it('renders a warning as its own text', () => {
    expect(plugin.transcript.notificationEntry?.(warning)).toEqual([
      { kind: 'text', text: 'Guidance was not applied because the turn ended before it could be processed.' },
    ])
    expect(plugin.transcript.classify(input(warning, undefined, AgentProvider.REASONIX)).kind).toBe('notification')
  })

  it('renders a compaction note as an end boundary', () => {
    expect(plugin.transcript.notificationEntry?.(compacted)).toEqual([
      { kind: 'compaction', phase: 'end', outcome: 'completed' },
    ])
    expect(plugin.session?.compactionBoundaryFromMessage?.(input(compacted))).toEqual({})
  })

  it('leaves model chunks and other frames unknown', () => {
    expect(reasonixNotificationEntry({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'A model answer.' } })).toEqual([])
    expect(reasonixNotificationEntry({ sessionUpdate: 'session_info_update' })).toEqual([])
  })
})
