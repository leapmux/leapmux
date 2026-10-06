import type { NativeCompactionOptions, NativeCompactionResult } from '../helpers/manualCompaction'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { COMPACTION_NEWER_ANSWER, exerciseNativeCompaction } from '../helpers/manualCompaction'
import { nativeScenarioModelContextText } from '../helpers/nativeScenario'

/** The native compaction of GitHub Copilot. Its summarizer takes the next ordered step. */
export const COPILOT_COMPACTION: NativeCompactionOptions = { summary: { route: 'queued' } }

/**
 * Run the native compaction of GitHub Copilot. Copilot replaces the whole history with the summary, so the next turn
 * carries no earlier answer, the newer one included.
 */
export async function exerciseCopilotCompaction(context: NativeScenarioContext): Promise<NativeCompactionResult> {
  const result = await exerciseNativeCompaction(context, COPILOT_COMPACTION)
  expect(nativeScenarioModelContextText(context, result.nextRequest)).not.toContain(COMPACTION_NEWER_ANSWER)
  return result
}
