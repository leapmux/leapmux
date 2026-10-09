import { describe, expect, it } from 'vitest'
import { input } from '~/components/chat/providers/testUtils'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow } from '~/test-support/toolCallFixture'
import { classifyMuseMessage } from './classification'
import '~/components/chat/providers'
import '~/components/chat/providers/testMocks'

describe('classifyMuseMessage', () => {
  it.each(['pending', 'inProgress', 'completed', 'cancelled'])('hides the recognized native %s list update from chat', (status) => {
    const frame = { method: 'session/todoListChanged', params: { sessionId: 'native-session', items: [{ text: 'Native task', status }] } }
    const original = structuredClone(frame)
    expect(providerRow(AgentProvider.MUSE_CODE, frame)).toEqual({ kind: 'hidden' })
    expect(frame).toEqual(original)
  })

  it('hides an actual native clear while the sidebar owns the list', () => {
    expect(providerRow(AgentProvider.MUSE_CODE, { method: 'session/todoListChanged', params: { items: [] } })).toEqual({ kind: 'hidden' })
  })

  it('shows an unknown native list status through the neutral notification model', () => {
    const frame = { method: 'session/todoListChanged', params: { items: [{ text: 'Native task', status: 'futureStatus' }] } }
    expect(providerRow(AgentProvider.MUSE_CODE, frame)).toEqual({
      kind: 'notification',
      thread: { entries: [{ kind: 'text', text: 'Unknown Muse to-do status: futureStatus' }] },
    })
  })

  it('keeps consolidated recognized native list updates hidden', () => {
    const frame = { type: 'notification_thread', messages: [
      { method: 'session/todoListChanged', params: { items: [{ text: 'Native task', status: 'completed' }] } },
      { method: 'session/todoListChanged', params: { items: [] } },
    ] }
    const original = structuredClone(frame)
    expect(classifyMuseMessage(input(undefined, { old_seqs: [], messages: frame.messages }, AgentProvider.MUSE_CODE))).toEqual({ kind: 'hidden' })
    expect(frame).toEqual(original)
  })
})

describe('classifyMuseMessage native outcomes', () => {
  it.each([
    ['answered', 'Muse question answered.'],
    ['cancelled', 'Muse question cancelled.'],
    ['interrupted', 'Muse question interrupted.'],
    ['clarified', 'Muse question clarified.'],
    ['timedOut', 'Muse question timed out.'],
    ['aborted', 'Muse question aborted.'],
    ['futureOutcome', 'Muse question settled: futureOutcome'],
  ])('classifies native question outcome %s through the neutral notification model', (outcome, text) => {
    const frame = { method: 'userInput/settled', params: { sessionId: 'native-session', userInputId: 'native-input', outcome } }
    const original = structuredClone(frame)
    expect(providerRow(AgentProvider.MUSE_CODE, frame)).toEqual({ kind: 'notification', thread: { entries: [{ kind: 'text', text }] } })
    expect(frame).toEqual(original)
  })

  it.each([
    ['noop', 'Muse compaction did not change the context.'],
    ['failed', 'Muse compaction failed.'],
    ['cancelled', 'Muse compaction cancelled.'],
  ])('classifies native compaction outcome %s through the neutral notification model', (outcome, text) => {
    const frame = { method: 'item/completed', params: { sessionId: 'native-session', item: { itemId: 'compaction', kind: 'compaction', outcome } } }
    const original = structuredClone(frame)
    expect(providerRow(AgentProvider.MUSE_CODE, frame)).toEqual({ kind: 'notification', thread: { entries: [{ kind: 'text', text }] } })
    expect(frame).toEqual(original)
  })
})
