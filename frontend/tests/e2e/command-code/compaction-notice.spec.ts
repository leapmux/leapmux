import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { compactionIndicator, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { assistantBubbles, chatScrollContainer, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { exerciseCommandCodeCompaction } from './compactionScenarios'

commandCodeTest('runs the actual native summarizer and retains the completed compaction notice', async ({ native }) => {
  await exerciseCommandCodeCompaction(native, {
    summaryGate: 'command-code-manual-summary',
    onHeld: async () => { await expect(compactionIndicator(native.page)).toBeVisible() },
  })
  await expectCompactionNoticeAfterReload(native.page, { count: 1, divider: true })
  await expectNoLiveCompaction(native.page)
})

commandCodeTest('keeps the answer visible after a large preparation message clears the composer', async ({ native }) => {
  const history = Array.from({ length: 60 }, (_, index) => `Native preparation line ${index}.`).join('\n\n')
  await sendNativeAnswer(native, 'Prepare enough native history to scroll.', history)
  await sendNativeAnswer(native, `Recent native task ${'padding '.repeat(18000)}`, 'The large preparation turn completed.', { entry: 'insert' })
  await expect.poll(() => chatScrollContainer(native.page).evaluate(element => element.scrollHeight - element.scrollTop - element.clientHeight))
    .toBeLessThanOrEqual(1)
})

commandCodeTest('ends an automatic compaction that has too little history to summarize', async ({ native }) => {
  const { page, modelScript } = native
  // The native run2 probe triggers compaction from reported usage, then declines the short actual history.
  const start = await modelScript.queue(
    { text: 'The reported usage is near the context limit.', usage: { inputTokens: 130000, outputTokens: 10 } },
    { text: 'The turn after the skipped compaction completed.' },
  )
  await sendMessage(page, modelScript.prompt('Report the scripted high usage.'))
  await modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(page)
  await sendMessage(page, modelScript.prompt('Continue with the short actual history.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  await expect(messageContents(page).filter({ hasText: 'Context compaction skipped' })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: 'The turn after the skipped compaction completed.' })).toHaveCount(1)
  await expectNoLiveCompaction(page)
  await sendNativeAnswer(native, 'Continue after the skipped compaction.', 'The queue released the next message.')
})
