/** Droid reports these retry reasons. The browser supplies their display text. */
export const DROID_LLM_RETRY_REASON_LABEL: ReadonlyMap<string, string> = new Map([
  ['rate_limited', 'rate limited'],
  ['overloaded', 'server overloaded'],
  ['timeout', 'request timed out'],
  ['network', 'network error'],
  ['empty_response', 'empty response'],
  ['unknown', 'unknown error'],
])
