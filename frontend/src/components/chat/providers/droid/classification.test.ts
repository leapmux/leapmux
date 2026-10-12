import { describe, expect, it } from 'vitest'
import { DROID_NOTIFICATION } from '~/generated/contracts/droid-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyDroidMessage } from './classification'

describe('classifyDroidMessage', () => {
  const compacted = { type: 'session_compacted', summaryId: 'summary-1', removedCount: 3 }

  it('draws the native compaction as a structured notice', () => {
    expect(classifyDroidMessage(input(compacted, null, AgentProvider.DROID))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'compaction', phase: 'end' }],
    })
  })

  it('keeps a compaction after another notice in one stored thread', () => {
    const wrapper = { old_seqs: [], messages: [{ type: 'settings_updated', settings: {} }, compacted] }
    expect(classifyDroidMessage(input(wrapper, wrapper, AgentProvider.DROID))).toEqual({
      kind: 'notification',
      entries: [{ kind: 'compaction', phase: 'end' }],
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
  ])('hides routine native %s frames', (type) => {
    expect(classifyDroidMessage(input({ type, requestId: '1' }, null, AgentProvider.DROID)))
      .toEqual({ kind: 'hidden' })
  })

  it('shows a native error without its JSON envelope', () => {
    expect(classifyDroidMessage(input({ type: DROID_NOTIFICATION.Error, message: 'The tool failed.' }, null, AgentProvider.DROID)))
      .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'The tool failed.' }] })
  })
})
