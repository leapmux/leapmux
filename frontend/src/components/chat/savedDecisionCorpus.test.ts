import type { PersistedControlResponse } from './persistedControlResponse'
import { describe, expect, it } from 'vitest'
import { SAVED_DECISION_CORPUS } from '~/test-support/savedDecisionCorpus'
import { resolveControlResponseSummary } from './persistedControlResponse'
import { pluginFor } from './providers/registry'
import './providers'

/** A snake_case identifier, which is a wire vocabulary rather than a reader's. */
const WIRE_TOKEN = /^[a-z][a-z0-9]*(?:_[a-z0-9]+)+$/

/**
 * A saved decision reads the words its own control carried, derived from the two halves
 * the worker stored beside it and from nothing else.
 *
 * The corpus holds payloads captured from the installed runtimes (RL-001), so this is the
 * reload half of the rule that `controls/permissionActionsParity.test.tsx` holds for the
 * BUTTONS. Each native wire shape of the corpus reaches one chokepoint here, and a
 * runtime that changes its shape fails the case instead of quietly degrading a reloaded
 * row to "Responded".
 */
describe('saved decision corpus', () => {
  it.each(SAVED_DECISION_CORPUS)('reads $name back as its own words', (entry) => {
    const display = resolveControlResponseSummary(
      storedResponse(entry.request, entry.response),
      pluginFor(entry.provider)?.controls?.controlResponseDisplay,
    )
    expect(display).toEqual({ kind: 'label', text: entry.label })
  })

  // The words for an option the agent named come from the stored REQUEST, and a row can
  // outlive it: an enrichment that failed validation is dropped while the original
  // message is kept. What the reader must never see then is the wire token the answer
  // carried -- `allow_once` where "Allow once" belongs. Each provider degrades to its own
  // canonical words or to the neutral fallback, and none to a raw token.
  it.each(SAVED_DECISION_CORPUS)('shows no wire token for $name when the request is absent', (entry) => {
    const display = resolveControlResponseSummary(
      storedResponse(undefined, entry.response),
      pluginFor(entry.provider)?.controls?.controlResponseDisplay,
    )
    expect(display.kind).toBe('label')
    if (display.kind === 'label') {
      expect(display.text).not.toBe('')
      expect(display.text).not.toMatch(WIRE_TOKEN)
    }
  })
})

/** One stored row, as `parsePersistedControlResponse` would hand it to the renderer. */
function storedResponse(
  request: Record<string, unknown> | undefined,
  response: Record<string, unknown>,
): PersistedControlResponse {
  // The claim token never reaches a derivation. It is present because the type requires
  // it, and its value is deliberately not one.
  return { requestId: 'request', claimToken: 'not-a-token', request, response }
}
