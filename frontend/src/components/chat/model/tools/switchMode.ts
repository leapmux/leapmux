import type { ProseResult } from '../toolCall'

export interface SwitchModeRequest {
  mode?: string
  target?: string
  /**
   * The words the outcome header states when the reader DECLINED this call.
   *
   * One tool's refusal is an ANSWER rather than a failure: the reader sends a plan back
   * with feedback, which is what the agent asked for. Only the provider that read the
   * frame knows which tool that is, so the neutral header cannot decide it -- it words
   * every other refusal "Declined", and this field is how a provider overrides that one
   * word for the one tool it holds.
   */
  declinedTitle?: string
}
export type SwitchModeResult = ProseResult
