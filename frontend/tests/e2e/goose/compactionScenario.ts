import type { NativeCompactionOptions } from '../helpers/manualCompaction'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { assistantBubbles } from '../helpers/ui'

/**
 * The native compaction of Goose.
 * Goose quotes the conversation, with its scenario markers, in the system prompt of the summary request. That request
 * has one user text, the fixed instruction below, which holds no marker, so a rule answers it. Goose reads the summary
 * from a JSON block after its analysis.
 */
export const GOOSE_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'rule', when: { user: '^Please summarize the conversation history provided in the system prompt\\.$' } },
  summaryEnvelope: summary => [
    '<analysis>Keep the earlier topic.</analysis>',
    '```json',
    JSON.stringify({ user_intent: [summary], current_work: 'Continue the test.' }),
    '```',
  ].join('\n'),
}

/** The message that Goose writes when its compaction ends. Goose draws it as an answer, not as a notice row. */
const GOOSE_COMPACTION_COMPLETE = 'Compaction complete'

/** Prove the actual Goose summary, the changed context of its next native turn, and its completion message after a reload. */
export async function exerciseGooseCompaction(context: NativeScenarioContext): Promise<void> {
  await exerciseNativeCompaction(context, GOOSE_COMPACTION)
  const complete = () => assistantBubbles(context.page).filter({ hasText: GOOSE_COMPACTION_COMPLETE }).first()
  await expect(complete()).toBeVisible()
  await context.page.reload()
  await expect(complete()).toBeVisible()
}
