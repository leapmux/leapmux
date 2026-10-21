import type { NativeCompactionHold, NativeCompactionOptions } from '../helpers/manualCompaction'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'

/**
 * The native compaction of CodeBuddy Code.
 * `/compact` is CodeBuddy's own slash command. Its summarizer takes the next ordered step, and CodeBuddy rejects a
 * summary reply without its native XML envelope.
 */
export const CODEBUDDY_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'queued' },
  summaryEnvelope: summary => `<conversation_history_summary><summary>${summary}</summary></conversation_history_summary>`,
}

/** Prove the native summarizer request and the replaced context, and require that no compaction notice shows. */
export async function exerciseContextCompaction(context: NativeScenarioContext, hold: NativeCompactionHold = {}): Promise<MockModelRequestRecord | undefined> {
  const { summaryRequest } = await exerciseNativeCompaction(context, { ...CODEBUDDY_COMPACTION, ...hold })
  if (!summaryRequest)
    throw new Error('The queued CodeBuddy compaction returned no summarizer request.')
  // CodeBuddy sends its summarizer request as its native compact agent.
  expect(JSON.stringify(summaryRequest.body)).toContain('"agent":"compact"')
  return summaryRequest
}
