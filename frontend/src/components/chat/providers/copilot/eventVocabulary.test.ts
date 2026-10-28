import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT, COPILOT_EVENT_PREFIX } from '~/generated/contracts/copilot-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

// The Worker consumes internal events before storage.
// Stored copies stay unknown unless the shared trace-prefix table hides them.
// This sweep rejects browser-only hide rules for those events.

/**
 * The payload each type needs to produce its row.
 *
 * A row that carries nothing to show is HIDDEN on purpose -- an empty assistant
 * message and a failure with no message both draw nothing -- so a sweep over empty
 * payloads would assert that rule rather than the classification this test is about.
 */
const PAYLOAD: Record<string, Record<string, unknown>> = {
  [COPILOT_EVENT.AssistantMessage]: { content: 'Done.' },
  [COPILOT_EVENT.AssistantReasoning]: { content: 'Let me consider...' },
  [COPILOT_EVENT.ModelCallFailure]: { errorMessage: 'the upstream model refused' },
  [COPILOT_EVENT.SessionError]: { message: 'An error.' },
  [COPILOT_EVENT.SessionWarning]: { message: 'A warning.' },
  [COPILOT_EVENT.SessionInfo]: { message: 'Information.' },
  [COPILOT_EVENT.SystemNotification]: { content: 'A system notice.' },
  [COPILOT_EVENT.SessionCustomNotification]: { name: 'An extension notice.' },
  [COPILOT_EVENT.PermissionCompleted]: { result: { kind: 'denied-by-rules' } },
  [COPILOT_EVENT.SkillInvoked]: { skill: 'review' },
  [COPILOT_EVENT.SkillInvokedRef]: { skill: 'review' },
}

function classify(type: string): string {
  const plugin = providerFor(AgentProvider.GITHUB_COPILOT)!
  const frame = {
    jsonrpc: '2.0',
    method: 'session.event',
    params: { sessionId: 's1', event: { id: 'e1', type, agentId: '', data: PAYLOAD[type] ?? {} } },
  }
  return plugin.transcript.classify(input(frame, undefined, AgentProvider.GITHUB_COPILOT)).kind
}

/**
 * The types that produce a row of their own rather than a notification or a hidden
 * row. Each is asserted by its own test elsewhere; this set keeps them out of the
 * sweep, which is about the types that have NO row.
 */
const STRUCTURAL: Record<string, string> = {
  [COPILOT_EVENT.AssistantMessage]: 'assistant_text',
  [COPILOT_EVENT.AssistantReasoning]: 'assistant_thinking',
  [COPILOT_EVENT.ToolStarted]: 'tool_use',
  [COPILOT_EVENT.ToolCompleted]: 'tool_result',
  [COPILOT_EVENT.SessionIdle]: 'result_divider',
}

describe('copilot event vocabulary', () => {
  it('reads the whole table the contract holds', () => {
    // Preserve the original minimum vocabulary and the six shared prefix families.
    expect(Object.keys(COPILOT_EVENT).length).toBeGreaterThanOrEqual(111)
    expect(Object.keys(COPILOT_EVENT_PREFIX).length).toBe(6)
  })

  it.each(Object.values(COPILOT_EVENT))('classifies %s without an obsolete hide rule', (type) => {
    const kind = classify(type)
    const expected = STRUCTURAL[type]
    if (expected) {
      expect(kind).toBe(expected)
      return
    }
    if (Object.values(COPILOT_EVENT_PREFIX).some(prefix => type.startsWith(prefix)))
      expect(kind).toBe('hidden')
    else
      expect(kind, `${type} has an obsolete browser hide rule`).not.toBe('hidden')
  })

  // A family the contract covers by PREFIX must answer for a member the table does
  // not list -- that is the whole point of holding the prefix instead of 21 members.
  it.each([
    ['model.call_finished', COPILOT_EVENT_PREFIX.ModelTrace],
    ['hook.start', COPILOT_EVENT_PREFIX.Hook],
    ['session.canvas.opened', COPILOT_EVENT_PREFIX.Canvas],
    ['factory.run_started', COPILOT_EVENT_PREFIX.Factory],
    ['assistant.fusion_phase_started', COPILOT_EVENT_PREFIX.AssistantFusion],
    ['session.fusion_completed', COPILOT_EVENT_PREFIX.SessionFusion],
  ])('hides %s through its family prefix', (type, prefix) => {
    expect(type.startsWith(prefix)).toBe(true)
    expect(classify(type)).toBe('hidden')
  })

  // The Worker consumes model failures before storage. The shared prefix rule remains.
  it('uses the shared trace rule for a stored model failure', () => {
    expect(COPILOT_EVENT.ModelCallFailure.startsWith(COPILOT_EVENT_PREFIX.ModelTrace)).toBe(true)
    expect(classify(COPILOT_EVENT.ModelCallFailure)).toBe('hidden')
  })

  // A type from a release later than this one still has to render as something a
  // reader can read. It takes the unknown row, which draws the payload -- that is
  // the honest answer, and it is what makes the sweep above worth having.
  it('leaves a type no release declared as unknown', () => {
    expect(classify('session.a_type_from_a_later_release')).toBe('unknown')
  })
})
