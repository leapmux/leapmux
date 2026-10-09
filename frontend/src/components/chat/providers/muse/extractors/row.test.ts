import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow } from '~/test-support/toolCallFixture'
import '~/components/chat/providers'
import '~/components/chat/providers/testMocks'

function nativeItem(kind: string, fields: Record<string, unknown> = {}) {
  return { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'native-item', kind, status: 'completed', ...fields } } }
}

describe('museExtractRow', () => {
  it('keeps the complete native assistant text', () => {
    expect(providerRow(AgentProvider.MUSE_CODE, nativeItem('agentMessage', { text: '  Native\ntext  ' })))
      .toEqual({ kind: 'assistant-text', text: '  Native\ntext  ' })
  })

  it('keeps reasoning summary segments in native order', () => {
    expect(providerRow(AgentProvider.MUSE_CODE, nativeItem('reasoning', { summary: ['First', 'Second'] })))
      .toEqual({ kind: 'assistant-thinking', text: 'First\n\nSecond' })
  })

  it('keeps a native empty user message distinct from an absent message', () => {
    expect(providerRow(AgentProvider.MUSE_CODE, nativeItem('userMessage', { text: '' })))
      .toEqual({ kind: 'user', text: '', attachments: [] })
  })

  it('reads a Worker user message through the shared reader', () => {
    const row = providerRow(AgentProvider.MUSE_CODE, { content: 'User text' })
    expect(row?.kind).toBe('user')
    if (row?.kind !== 'user')
      throw new Error('The user message requires a user row.')
    expect(row.text).toBe('User text')
  })

  it.each(['agentMessage', 'reasoning'])('hides empty native %s content', (kind) => {
    expect(providerRow(AgentProvider.MUSE_CODE, nativeItem(kind, { text: '' }))).toEqual({ kind: 'hidden' })
  })

  it.each(['subagent', 'reminderChild'])('keeps native %s state on its sidebar surface', (kind) => {
    expect(providerRow(AgentProvider.MUSE_CODE, nativeItem(kind))).toEqual({ kind: 'hidden' })
  })

  it('keeps an active workflow hidden until its result arrives', () => {
    expect(providerRow(AgentProvider.MUSE_CODE, nativeItem('workflow', { status: 'inProgress' }))).toEqual({ kind: 'hidden' })
  })

  it.each(['hookRun', 'futureItem'])('returns no invented row for native %s', (kind) => {
    expect(providerRow(AgentProvider.MUSE_CODE, nativeItem(kind))).toBeNull()
  })
})
