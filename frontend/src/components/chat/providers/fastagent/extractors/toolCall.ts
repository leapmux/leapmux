import type { ToolCallSpec } from '../../../model/toolCall'
import type { ACPToolCallAdapter, ACPToolFacts } from '../../acp/extractors/toolCall'
import { declinedToolCallSpec } from '../../declinedToolCall'
import { isFastAgentRefusalSentence } from '../protocol'

/**
 * Fast Agent's own reading of one call.
 *
 * Fast Agent hooks no fact of its frame: the shared build reads every call. The adapter
 * adds one post-condition around that build, for the reason `providers/README.md` gives:
 * a refused call of every tool must read `declined`, with the refusal as its body.
 */
export const fastAgentToolCallAdapter: ACPToolCallAdapter = (facts, base): ToolCallSpec => {
  const spec = base()
  return fastAgentRefused(facts) ? declinedToolCallSpec(spec, facts.text) : spec
}

/**
 * Whether Fast Agent refused this call on the reader's Deny answer.
 *
 * The frame's OWN status decides with the sentence: only a failed update states a
 * refusal, and a call that completed and printed the same words ran.
 */
function fastAgentRefused(facts: ACPToolFacts): boolean {
  return facts.lifecycle.frameStatus === 'failed' && isFastAgentRefusalSentence(facts.text)
}
