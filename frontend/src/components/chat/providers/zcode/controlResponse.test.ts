import type { PersistedControlResponse } from '../../persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { ZCODE_TOOL } from '~/generated/contracts/zcode-protocol'
import { buildAllowResponse, buildDenyResponse } from '~/utils/controlResponse'
import { zcodeControlResponseDisplay } from './controlResponse'
import { ZCODE_METHOD } from './protocol'

/**
 * A stored answer row. `response` is the SHARED allow/deny envelope: ZCode's own
 * reply shape is built by the worker when it forwards the answer, so it never
 * reaches the database.
 */
function cr(
  response: Record<string, unknown> | undefined,
  request?: Record<string, unknown>,
): PersistedControlResponse {
  return { provider: 'ZCODE', requestId: 'req-1', request, response }
}

/** The pruned request context for an AskUserQuestion, as the worker stores it. */
function questionRequest(...questions: string[]): Record<string, unknown> {
  return {
    method: ZCODE_METHOD.RequestUserInput,
    request: { tool_name: ZCODE_TOOL.AskUserQuestion },
    questions: questions.map(question => ({ question })),
  }
}

function allow(answers: Record<string, string>): Record<string, unknown> {
  return buildAllowResponse('req-1', { answers })
}

describe('zcodeControlResponseDisplay', () => {
  it('renders one answered question as a question: answer line', () => {
    expect(zcodeControlResponseDisplay(cr(
      allow({ 'Which database?': 'Postgres' }),
      questionRequest('Which database?'),
    ))).toEqual({ kind: 'label', text: 'Which database?: Postgres' })
  })

  // The REQUEST's order is authoritative, not the answer map's: object key order is a
  // detail of whoever serialized the map, and two rows of the same prompt must read
  // identically.
  it('orders the lines by the request question list, not by the answer map', () => {
    expect(zcodeControlResponseDisplay(cr(
      allow({ Second: 'b', First: 'a' }),
      questionRequest('First', 'Second'),
    ))).toEqual({ kind: 'label', text: 'First: a\nSecond: b' })
  })

  // A stale or absent request context must not lose the answer.
  it('appends an answer whose question the request does not name', () => {
    expect(zcodeControlResponseDisplay(cr(
      allow({ Known: 'a', Unlisted: 'b' }),
      questionRequest('Known'),
    ))).toEqual({ kind: 'label', text: 'Known: a\nUnlisted: b' })
  })

  it('renders the bare answer when the question text is empty', () => {
    expect(zcodeControlResponseDisplay(cr(allow({ '': 'Postgres' }), questionRequest(''))))
      .toEqual({ kind: 'label', text: 'Postgres' })
  })

  it('trims the stored answer text', () => {
    expect(zcodeControlResponseDisplay(cr(allow({ Q: '  A  ' }), questionRequest('Q'))))
      .toEqual({ kind: 'label', text: 'Q: A' })
  })

  // A blank answer is dropped because the app-server discards one too: showing it
  // would claim an answer that was never delivered.
  it('falls back to the neutral approved label when every answer is blank', () => {
    expect(zcodeControlResponseDisplay(cr(allow({ Q: '   ' }), questionRequest('Q'))))
      .toEqual({ kind: 'label', text: 'Approved' })
  })

  it('falls back to the neutral label when the allow carries no answers at all', () => {
    expect(zcodeControlResponseDisplay(cr(buildAllowResponse('req-1', {}), questionRequest('Q'))))
      .toEqual({ kind: 'label', text: 'Approved' })
  })

  // A permission decision and a plan approval are exactly what the neutral envelope
  // already describes, so the ZCode derivation must not dress them up as answers.
  it('delegates a permission allow to the neutral envelope', () => {
    expect(zcodeControlResponseDisplay(cr(
      allow({ 'Which database?': 'Postgres' }),
      { method: ZCODE_METHOD.RequestPermission, request: { tool_name: ZCODE_TOOL.Bash } },
    ))).toEqual({ kind: 'label', text: 'Approved' })
  })

  // The plan approval travels over the SAME method as a question, so the tool name is
  // what separates them.
  it('delegates a plan approval to the neutral envelope', () => {
    expect(zcodeControlResponseDisplay(cr(
      allow({ 'Review this implementation plan.': 'approve' }),
      { method: ZCODE_METHOD.RequestUserInput, request: { tool_name: ZCODE_TOOL.ExitPlanMode } },
    ))).toEqual({ kind: 'label', text: 'Approved' })
  })

  it('delegates when the request context is gone, so the answers cannot be attributed', () => {
    expect(zcodeControlResponseDisplay(cr(allow({ Q: 'A' }))))
      .toEqual({ kind: 'label', text: 'Approved' })
  })

  it('renders a bare denial as the neutral rejected label', () => {
    expect(zcodeControlResponseDisplay(cr(buildDenyResponse('req-1', ''), questionRequest('Q'))))
      .toEqual({ kind: 'label', text: 'Rejected' })
  })

  it('renders a denial with a typed reason as feedback', () => {
    expect(zcodeControlResponseDisplay(cr(
      buildDenyResponse('req-1', 'use MySQL instead'),
      questionRequest('Q'),
    ))).toEqual({ kind: 'feedback', message: 'use MySQL instead' })
  })

  // Null lets the shared chokepoint degrade to its generic label rather than render
  // an empty row.
  it('returns null for a payload that is not the behavior envelope', () => {
    expect(zcodeControlResponseDisplay(cr(undefined))).toBeNull()
    expect(zcodeControlResponseDisplay(cr({}))).toBeNull()
    expect(zcodeControlResponseDisplay(cr({ response: { response: { behavior: 'maybe' } } }))).toBeNull()
  })
})
