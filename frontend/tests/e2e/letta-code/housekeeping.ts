import type { MockModelRule } from '../helpers/mockModelScript'
import { MOCK_SESSION_TITLE } from '../helpers/mockModelScenario'

/**
 * Letta's session-title housekeeping turn, keyed off the request body so it
 * cannot consume a scripted step whatever slot the prompt sits in.
 *
 * Letta's local backend names a conversation through this turn, at a time that
 * the test does not control. The Letta test object registers the rule for every
 * test (`letta-fixtures.ts`), so a spec registers no copy of it. A spec that needs
 * another answer registers its own rule under another name: a newer rule of the
 * same priority matches first.
 *
 * The rule stays out of `HOUSEKEEPING_RULES` in `helpers/mockModelScenario.ts`,
 * which every scenario of every provider holds. Its body matcher would also
 * answer a content turn of another provider whose request holds the words
 * "session title".
 */
export const LETTA_TITLE_RULE: MockModelRule = {
  name: 'title-letta',
  when: { body: 'session title' },
  respond: { text: MOCK_SESSION_TITLE },
}
