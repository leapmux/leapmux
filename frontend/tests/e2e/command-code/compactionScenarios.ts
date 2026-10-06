import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { expectCompactionNotice } from '../helpers/compaction'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { uniqueMarker } from '../helpers/shellArguments'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

/** Supply real conversation context before the native summarizer runs. */
export async function exerciseCommandCodeCompaction(context: ManagedNativeScenarioContext): Promise<void> {
  const marker = uniqueMarker()
  const old = `COMMANDCODEOLD${marker}`
  for (let index = 0; index < 6; index++) {
    // The native compactor keeps the last 30000 tokens (`keepRecentTokens`) and summarizes the earlier turns.
    // The last turn holds that much text, and typing it outlasts the test, so the helper inserts it.
    const recent = index === 5
    await sendNativeAnswer(context, index === 0 ? `Keep ${old} in the old task.` : recent ? `Recent native task ${'padding '.repeat(18000)}` : `Prepare native compaction turn ${index}.`, `The native preparation turn ${index} completed.`, { entry: recent ? 'insert' : 'type' })
  }
  const summary = `COMMANDCODESUMMARY${marker}`
  const start = (await context.modelScript.status()).stepCount
  await context.modelScript.queue({ text: summary })
  await sendMessage(context.page, '/compact')
  const status = await context.modelScript.waitForSteps(start + 1)
  const request = status.requests.find(record => record.stepIndex === start)
  if (!request)
    throw new Error('The native compactor sent no model request.')
  expect(nativeModelContextText(request)).toContain(old)
  await waitForAgentIdle(context.page)
  await expectCompactionNotice(context.page)
  const next = await sendNativeAnswer(context, 'Continue after native compaction.', 'The native compacted task continued.')
  expect(nativeModelContextText(next)).toContain(summary)
  expect(nativeModelContextText(next)).not.toContain(old)
}
