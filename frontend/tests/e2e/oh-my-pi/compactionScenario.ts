import type { NativeCompactionOptions, NativeCompactionResult } from '../helpers/manualCompaction'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'

/**
 * The native compaction of Oh My Pi. The handoff summary request falls through to the fallback, which answers the
 * summary.
 */
export const OH_MY_PI_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', requestMarker: 'You MUST summarize the conversation above into a structured handoff summary' },
}

/**
 * Run the native compaction of Oh My Pi, and prove its one short summary.
 *
 * omp sends two summary requests with one system prompt: the handoff summary, then a short summary that ends with its
 * own instruction. A rule takes the short summary alone, and the handoff summary reaches the fallback of
 * {@link OH_MY_PI_COMPACTION}.
 */
export async function exerciseOhMyPiCompaction(context: NativeScenarioContext): Promise<NativeCompactionResult> {
  const shortSummary = `native-short-summary-${randomUUID()}`
  await context.modelScript.rule({
    name: shortSummary,
    when: { system: 'Summarize user.AI coding-assistant conversations', user: 'Summarize conversation changes as a pull request description' },
    respond: { text: 'I recorded the older context, a newer note, and the current note.' },
  })
  const result = await exerciseNativeCompaction(context, OH_MY_PI_COMPACTION)
  expect((await context.modelScript.status()).ruleMatches[shortSummary], 'omp asks for one short summary for each compaction').toBe(1)
  return result
}
