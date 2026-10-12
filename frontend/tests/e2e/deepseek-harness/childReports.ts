import type { MockModelRule } from '../helpers/mockModelScript'
import { escapeRegExp } from '../../../src/lib/regexp'

/** Match only the native parent report for the exact child Session. */
export function deepseekHarnessChildReportRule(childSessionId: string): MockModelRule {
  if (!childSessionId || childSessionId.includes('\0'))
    throw new Error('The DeepSeek Harness child report requires its exact native Session identity.')
  const exactId = escapeRegExp(childSessionId)
  return {
    name: `the native parent report for ${childSessionId}`,
    when: { lastMessage: { role: 'user', text: `^Background subagent ${exactId} (?:finished and will do no further work unless you send it more|was stopped before it finished)\\.` } },
    respond: { text: 'The native parent consumed its exact child report.' },
  }
}
