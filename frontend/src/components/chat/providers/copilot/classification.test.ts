import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT } from '~/generated/contracts/copilot-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { classifyCopilotMessage } from './classification'

describe('classifyCopilotMessage', () => {
  it.each([
    'command.execute',
    'session_limits_exhausted.requested',
    'system.message',
    'session.compaction_start',
    'session.start',
    'user_input.completed',
    'audio',
  ])('leaves a stored %s row unknown', (type) => {
    const frame = { method: 'session.event', params: { event: { type, data: {} } } }
    expect(classifyCopilotMessage(input(frame, undefined, AgentProvider.GITHUB_COPILOT))).toEqual({ kind: 'unknown' })
  })

  it('reads the referenced skill notice', () => {
    const frame = { method: 'session.event', params: { event: { type: COPILOT_EVENT.SkillInvokedRef, data: { skill: 'review' } } } }
    expect(classifyCopilotMessage(input(frame, undefined, AgentProvider.GITHUB_COPILOT)))
      .toEqual({ kind: 'notification', entries: [{ kind: 'text', text: 'Loaded the skill review' }] })
  })

  it('hides the sql to-do snapshot row', () => {
    expect(classifyCopilotMessage(input({ type: 'leapmux_copilot_sql_todos', rows: [] }, undefined, AgentProvider.GITHUB_COPILOT)))
      .toEqual({ kind: 'hidden' })
  })
})
