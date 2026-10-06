import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { expectCompactionNotice } from '../helpers/compaction'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { uniqueMarker } from '../helpers/shellArguments'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { deepseekHarnessModelContextText } from './modelContextText'

/** Run the native summarizer and prove that its checkpoint replaces the older model context. */
export async function exerciseDeepseekHarnessCompaction(context: ManagedNativeScenarioContext): Promise<void> {
  const marker = uniqueMarker()
  const old = `DEEPSEEKOLD${marker}`
  for (let index = 0; index < 6; index++) {
    await sendNativeAnswer(context, index === 0 ? `Keep ${old} in the older task.` : `Prepare native compaction turn ${index}.`, `The native preparation turn ${index} completed.`)
  }
  const summary = `DEEPSEEKSUMMARY${marker}`
  const start = (await context.modelScript.status()).stepCount
  await context.modelScript.queue({ text: summary })
  await sendMessage(context.page, '/compact')
  const status = await context.modelScript.waitForSteps(start + 1)
  const request = status.requests.find(row => row.stepIndex === start)
  if (!request)
    throw new Error('The native compactor sent no exact model request.')
  expect(request.protocol).toBe('anthropic-messages')
  expect(deepseekHarnessModelContextText(request)).toContain('You are now acting as a compaction engine')
  expect(deepseekHarnessModelContextText(request)).toContain(old)
  await waitForAgentIdle(context.page)
  await expectCompactionNotice(context.page)
  const next = await sendNativeAnswer(context, 'Continue after the native context checkpoint.', 'The native compacted context continued.')
  expect(deepseekHarnessModelContextText(next)).toContain(summary)
  expect(deepseekHarnessModelContextText(next)).not.toContain(old)
}
