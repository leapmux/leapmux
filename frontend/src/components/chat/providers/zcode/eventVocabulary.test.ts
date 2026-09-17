import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

// Every event type the app-server sends must reach a row the reader can read. A type
// that no rule covers classifies as `unknown`, and the transcript draws it as a
// raw-JSON bubble that says nothing a reader can act on.
//
// The known world is the generated `ZCODE_EVENT` table, whose values come from
// contracts/zcode-protocol.json -- the Go worker reads the same names, so a type
// added there is a type both sides see.

/**
 * The payload each type needs to produce its row.
 *
 * A row carrying nothing to show is hidden on purpose, so a sweep over empty
 * payloads would assert that rule rather than the classification this test is about.
 */
const PAYLOAD: Record<string, Record<string, unknown>> = {
  // `session.updated` is the app-server's catch-all, and a MODEL RESPONSE is the one
  // variant that carries assistant text. It states a `stopReason` beside its content,
  // which is what tells it from the telemetry variants that share the type.
  [ZCODE_EVENT.SessionUpdated]: { content: 'Done.', stopReason: 'end_turn' },
  [ZCODE_EVENT.ToolUpdated]: {
    kind: ZCODE_TOOL_KIND.Scheduled,
    toolCallId: 'z1',
    toolName: 'Read',
    input: { file_path: '/repo/a.ts' },
  },
  [ZCODE_EVENT.TurnCompleted]: { resultType: 'success', duration: 1200 },
  [ZCODE_EVENT.TurnFailed]: { error: { message: 'the model refused' } },
  [ZCODE_EVENT.PermissionResolved]: { decision: 'allow', toolName: 'Read' },
}

function classify(type: string): string {
  const plugin = providerFor(AgentProvider.ZCODE)!
  return plugin?.transcript.classify(input({ type, payload: PAYLOAD[type] ?? {} }, undefined, AgentProvider.ZCODE)).kind
}

/**
 * The types that produce a row of their own rather than a notification or a hidden
 * row. Each is asserted by its own test elsewhere; this set keeps them out of the
 * sweep, which is about the types that have NO row.
 */
const STRUCTURAL: Record<string, string> = {
  [ZCODE_EVENT.SessionUpdated]: 'assistant_text',
  [ZCODE_EVENT.ToolUpdated]: 'tool_use',
  [ZCODE_EVENT.TurnCompleted]: 'result_divider',
  [ZCODE_EVENT.TurnFailed]: 'result_divider',
}

describe('zcode event vocabulary', () => {
  it('reads the whole table the contract holds', () => {
    expect(Object.keys(ZCODE_EVENT).length).toBeGreaterThanOrEqual(25)
  })

  it.each(Object.values(ZCODE_EVENT))('names %s', (type) => {
    const kind = classify(type)
    const expected = STRUCTURAL[type]
    if (expected) {
      expect(kind).toBe(expected)
      return
    }
    expect(
      kind,
      `${type} reaches no rule, so the row draws raw JSON. Add it to `
      + 'ZCODE_HIDDEN_TYPES or ZCODE_NOTIFICATION_TYPES.',
    ).not.toBe('unknown')
  })

  // A type from a release later than this one still has to render as something. It
  // takes the unrecognized row, which draws the payload -- the honest answer, and
  // what makes the sweep above worth having.
  it('leaves an event no release declared as unknown', () => {
    expect(classify('session.somethingLater')).toBe('unknown')
  })
})
