import type { NativeCompactionOptions, NativeCompactionResult } from '../helpers/manualCompaction'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { COMPACTION_NEWER_ANSWER, exerciseNativeCompaction } from '../helpers/manualCompaction'
import { nativeScenarioModelContextText } from '../helpers/nativeScenario'

/**
 * The native compaction of Codex.
 * `/compact` starts a native compaction, and a rule answers its summarizer. Every pattern in the list must match the
 * native summary request. The context-compaction item of the CLI supplies the notice.
 */
export const CODEX_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'rule', when: { user: ['compact', 'summary'] } },
}

/**
 * Run the native compaction of Codex. Codex replaces the whole history with the summary, so the next turn carries no
 * earlier answer, the newer one included.
 */
export async function exerciseCodexCompaction(context: NativeScenarioContext): Promise<NativeCompactionResult> {
  const result = await exerciseNativeCompaction(context, CODEX_COMPACTION)
  expect(nativeScenarioModelContextText(context, result.nextRequest)).not.toContain(COMPACTION_NEWER_ANSWER)
  return result
}
