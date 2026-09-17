import { describe, expect, it } from 'vitest'
import { COPILOT_EVENT, COPILOT_EVENT_PREFIX } from '~/generated/contracts/copilot-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

// Every event type the installed runtime declares must reach a row the reader can
// read. The worker persists EVERY event the runtime does not mark ephemeral, and the
// browser draws a raw-JSON bubble for a type it cannot identify. The runtime declares 131
// types; LeapMux named 52, so an ordinary turn wrote several bubbles that said
// nothing a reader could act on.
//
// The known world is the generated `COPILOT_EVENT` table, transcribed from
// copilot-sdk's own session-event union. The sweep below walks it and fails for a
// type that reaches `unknown` -- which is what a new runtime release would produce.

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
  [COPILOT_EVENT.ModelCallFailure]: { message: 'the upstream model refused' },
}

function classify(type: string): string {
  const plugin = providerFor(AgentProvider.GITHUB_COPILOT)!
  const frame = {
    jsonrpc: '2.0',
    method: 'session.event',
    params: { sessionId: 's1', event: { id: 'e1', type, agentId: '', data: PAYLOAD[type] ?? {} } },
  }
  return plugin?.transcript.classify(input(frame)).kind
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
    // 111 named types plus the six prefix families the contract covers by rule.
    expect(Object.keys(COPILOT_EVENT).length).toBeGreaterThanOrEqual(111)
    expect(Object.keys(COPILOT_EVENT_PREFIX).length).toBe(6)
  })

  it.each(Object.values(COPILOT_EVENT))('names %s', (type) => {
    const kind = classify(type)
    const expected = STRUCTURAL[type]
    if (expected) {
      expect(kind).toBe(expected)
      return
    }
    expect(
      kind,
      `${type} reaches no rule, so the row draws raw JSON. Add it to `
      + 'COPILOT_HIDDEN_TYPES, COPILOT_NOTIFICATION_TYPES or COPILOT_CONTROL_REQUEST_TYPES.',
    ).not.toBe('unknown')
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

  // `model.call_failure` is the one member of a hidden family that LeapMux surfaces,
  // so the prefix rule must carve it out rather than swallow it.
  it('keeps the model-call failure visible inside a hidden family', () => {
    expect(COPILOT_EVENT.ModelCallFailure.startsWith(COPILOT_EVENT_PREFIX.ModelTrace)).toBe(true)
    expect(classify(COPILOT_EVENT.ModelCallFailure)).toBe('notification')
  })

  // A type from a release later than this one still has to render as something a
  // reader can read. It takes the unknown row, which draws the payload -- that is
  // the honest answer, and it is what makes the sweep above worth having.
  it('leaves a type no release declared as unknown', () => {
    expect(classify('session.a_type_from_a_later_release')).toBe('unknown')
  })
})
