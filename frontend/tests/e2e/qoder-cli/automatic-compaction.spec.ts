import { expect } from '@playwright/test'
import { expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { CLAUDE_SUMMARIZER_PATTERN } from '../helpers/manualCompaction'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { qoderTest } from '../qoder-fixtures'

qoderTest('ends automatic compaction with one boundary row and no live indicator', async ({ native }) => {
  const { page, modelScript } = native
  const summary = 'QODER_AUTOMATIC_SUMMARY'
  await modelScript.rule({
    name: 'qoder automatic summary',
    when: { body: CLAUDE_SUMMARIZER_PATTERN },
    // Automatic compaction replaces the current prompt too. Keep the scenario identity inside the retained summary.
    respond: { text: `<summary>${modelScript.prompt(`${summary} The earlier task is complete.`)}</summary>`, usage: { inputTokens: 8000, outputTokens: 30 } },
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
