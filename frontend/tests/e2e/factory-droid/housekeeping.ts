import type { MockModelRule } from '../helpers/mockModelScript'
import { MOCK_SESSION_TITLE } from '../helpers/mockModelScenario'

/**
 * Droid's session-title housekeeping turn.
 *
 * The CLI names a session through a helper whose system prompt opens with this
 * sentence. A mock that keyed the answer off call order would let this turn eat
 * the step the test scripted for the real one. The prompt is the anchor; the
 * research report captured it from a live mock request.
 */
const DROID_TITLE_PROMPT_FRAGMENT = 'session titles for a session picker'

/**
 * The rule that answers the title turn, so no scripted step is consumed by it.
 *
 * Matched on the request BODY rather than the system slot: the title helper's
 * prompt is the anchor whatever message slot carries it, and a real turn never
 * contains that sentence.
 *
 * The turn fires before the first real turn. The Droid test object registers the
 * rule for every test (`droid-fixtures.ts`), so a spec registers no copy of it.
 */
export const DROID_TITLE_RULE: MockModelRule = {
  name: 'title-droid',
  when: { body: DROID_TITLE_PROMPT_FRAGMENT },
  respond: { text: MOCK_SESSION_TITLE },
}
