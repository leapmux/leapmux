import { describe, expect, it } from 'vitest'
import { DROID_NOTIFICATION } from '~/generated/contracts/droid-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyDroidMessage } from './classification'

describe('classifyDroidMessage', () => {
  const compacted = { type: 'session_compacted', summaryId: 'summary-1', removedCount: 3 }

  it('leaves an old stored native user echo as unknown', () => {
    expect(classifyDroidMessage(input({ type: 'create_message', message: { role: 'user', content: [{ type: 'text', text: 'old echo' }] } }, null, AgentProvider.DROID))).toEqual({ kind: 'unknown' })
  })

  it('draws the native compaction as a structured notice', () => {
    expect(classifyDroidMessage(input(compacted, null, AgentProvider.DROID))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'compaction', phase: 'end', outcome: 'completed' }],
    })
  })

  it('keeps a compaction after another notice in one stored thread', () => {
    const wrapper = { old_seqs: [], messages: [{ type: 'error', message: 'A notice' }, compacted] }
    expect(classifyDroidMessage(input(wrapper, wrapper, AgentProvider.DROID))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'text', text: 'A notice' }, { kind: 'compaction', phase: 'end', outcome: 'completed' }],
    })
  })

  it.each([
    DROID_NOTIFICATION.WorkingStateChanged,
    DROID_NOTIFICATION.SettingsUpdated,
    DROID_NOTIFICATION.SessionTitleUpdated,
    DROID_NOTIFICATION.SessionTokenUsageChanged,
    DROID_NOTIFICATION.ToolExecutionPhaseChanged,
    DROID_NOTIFICATION.AssistantTextDelta,
    DROID_NOTIFICATION.AssistantTextComplete,
  ])('leaves an old stored %s frame visible as unknown', (type) => {
    expect(classifyDroidMessage(input({ type, requestId: '1' }, null, AgentProvider.DROID)))
      .toEqual({ kind: 'unknown' })
  })

  it('shows a native error without its JSON envelope', () => {
    expect(classifyDroidMessage(input({ type: DROID_NOTIFICATION.Error, message: 'The tool failed.' }, null, AgentProvider.DROID)))
      .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'The tool failed.' }] })
  })

  it('draws an unknown kind as the no-display card', () => {
    expect(classifyDroidMessage(input({ type: 'unknown_native_event', value: 1 }, null, AgentProvider.DROID)))
      .toEqual({ kind: 'unknown' })
    expect(classifyDroidMessage(input({ type: 'error' }, null, AgentProvider.DROID)))
      .toEqual({ kind: 'unknown' })
  })

  it('draws a user-only system notice', () => {
    expect(classifyDroidMessage(input({ type: 'create_message', message: {
      role: 'system',
      visibility: 'user_only',
      content: [{ type: 'text', text: 'Native notice' }],
    } }, null, AgentProvider.DROID)))
      .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Native notice' }] })
  })
})
