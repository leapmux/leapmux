import { describe, expect, it } from 'vitest'
import { PI_EVENT } from '~/generated/contracts/pi-protocol'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerFor } from '../registry'
import { input } from '../testUtils'
import './plugin'

// Every event type Pi sends must reach a row the reader can read. A type no rule
// names classifies `unknown`, and the transcript draws it as a raw-JSON bubble that
// says nothing a reader can act on.
//
// The known world is the generated `PI_EVENT` table, whose values come from
// contracts/pi-protocol.json -- the Go worker reads the same names, so a type added
// there is a type both sides see. The sweep walks it and fails for one that reaches
// `unknown`.
//
// The gap this guards is real and recent: the three `summarization_retry_*` events
// were persisted as notifications while no rule here named them, so each drew raw
// JSON, and a consolidated thread that held one rendered its first entry alone.

/**
 * The payload each type needs to produce its row.
 *
 * A row carrying nothing to show is hidden on purpose, so a sweep over empty
 * payloads would assert that rule rather than the classification this test is about.
 */
const PAYLOAD: Record<string, Record<string, unknown>> = {
  [PI_EVENT.MessageEnd]: {
    message: { role: 'assistant', content: [{ type: 'text', text: 'Done.' }] },
  },
  [PI_EVENT.ToolExecutionStart]: { toolCallId: 'call-1', toolName: 'read', args: { path: '/repo/a.ts' } },
  [PI_EVENT.ToolExecutionEnd]: { toolCallId: 'call-1', toolName: 'read', result: { content: [{ type: 'text', text: 'ok' }] } },
  [PI_EVENT.ExtensionUIRequest]: { method: 'notify', params: { message: 'the extension says so' } },
  [PI_EVENT.EntryAppended]: { entry: { type: 'model_change', modelId: 'claude-opus-5' } },
}

function classify(type: string): string {
  const plugin = providerFor(AgentProvider.PI)!
  return plugin?.transcript.classify(input({ type, ...PAYLOAD[type] }, undefined, AgentProvider.PI)).kind
}

/**
 * The types that produce a row of their own rather than a notification or a hidden
 * row. Each is asserted by its own test elsewhere; this set keeps them out of the
 * sweep, which is about the types that have NO row.
 */
const STRUCTURAL: Record<string, string> = {
  [PI_EVENT.MessageEnd]: 'assistant_text',
  [PI_EVENT.ToolExecutionStart]: 'tool_use',
  [PI_EVENT.ToolExecutionEnd]: 'tool_result',
  [PI_EVENT.AgentEnd]: 'result_divider',
}

describe('pi event vocabulary', () => {
  it('reads the whole table the contract holds', () => {
    expect(Object.keys(PI_EVENT).length).toBeGreaterThanOrEqual(27)
  })

  it.each(Object.values(PI_EVENT))('names %s', (type) => {
    const kind = classify(type)
    const expected = STRUCTURAL[type]
    if (expected) {
      expect(kind).toBe(expected)
      return
    }
    expect(
      kind,
      `${type} reaches no rule, so the row draws raw JSON. Add it to `
      + 'PI_HIDDEN_EVENT_TYPES or PI_NOTIFICATION_EVENT_TYPES.',
    ).not.toBe('unknown')
  })

  // A type from a release later than this one still has to render as something. It
  // takes the unrecognized row, which draws the payload -- the honest answer, and
  // what makes the sweep above worth having.
  it('leaves an event no release declared as unknown', () => {
    expect(classify('an_event_from_a_later_release')).toBe('unknown')
  })
})
