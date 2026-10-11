import type { NativeCompactionOptions } from '../helpers/manualCompaction'
import type { NativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { chatScrollContainer, sendMessage, waitForAgentIdle } from '../helpers/ui'

/** The native compaction of Pi. A rule answers its summarizer, which Pi gives its own system prompt. */
export const PI_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'rule', when: { system: '^You are a context summarization assistant\\.' } },
}

/**
 * Prove that Pi replaces the start of a compaction that it cannot run with its error.
 * A new session has no turn, so Pi refuses `/compact` without a model request, and the stored transcript keeps the
 * error and no start notice.
 */
export async function exercisePiEmptyCompaction(context: Pick<NativeScenarioContext, 'page'>): Promise<void> {
  const { page } = context
  await sendMessage(page, '/compact')
  const chat = chatScrollContainer(page)
  await expect(chat).toContainText('Nothing to compact (session too small)')
  await waitForAgentIdle(page)

  await page.reload()
  await expect(chat).toContainText('Nothing to compact (session too small)')
  await expect(chat).not.toContainText('Compacting context...')
}
