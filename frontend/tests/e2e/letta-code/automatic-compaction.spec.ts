import { expect } from '@playwright/test'
import { expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'

lettaTest('ends automatic compaction with one boundary row and no live indicator', async ({ native }) => {
  const { page, modelScript } = native
  const summary = 'LETTA_AUTOMATIC_SUMMARY'
  await modelScript.rule({
    name: 'letta automatic summary',
    priority: 'high',
    // Letta Code 0.34.15 uses LOCAL_ALL_COMPACTION_PROMPT or LOCAL_SLIDING_WINDOW_COMPACTION_PROMPT as its system prompt.
    when: { system: '(?:Your task is to create a detailed summary of the conversation so far|The following messages are being evicted from the BEGINNING of your context window)' },
    respond: { text: modelScript.prompt(`${summary} The first task is complete.`) },
  })
  const first = await modelScript.queue({ text: 'The first task is complete.', usage: { inputTokens: 5_000_000, outputTokens: 20 } })
  await sendMessage(page, modelScript.prompt('Complete the first scripted task.'))
  await modelScript.waitForSteps(first + 1)
  await waitForAgentIdle(page)
  const second = await modelScript.queue({ text: 'The next task is complete.' })
  await sendMessage(page, modelScript.prompt('Complete the next scripted task.'))
  await modelScript.waitForSteps(second + 1)
  await waitForAgentIdle(page)
  expect(JSON.stringify((await modelScript.requestAt(second)).body)).toContain(summary)
  await expect(assistantBubbles(page).filter({ hasText: 'The next task is complete.' })).toHaveCount(1)
  await expectCompactionNoticeAfterReload(page, { count: 1, divider: true })
  await expectNoLiveCompaction(page)
})
