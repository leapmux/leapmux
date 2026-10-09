/**
 * The installed Muse host refuses a manual compaction it cannot run
 * (`compaction_unavailable`), so no completed compaction boundary ever reaches the
 * transcript: the /compact command degrades to an ordinary message and no notice row
 * appears.
 */
import { expect } from '@playwright/test'
import { compactionNoticeRow } from '../helpers/compaction'
import { nativeModelContextText, nativeTextStep } from '../helpers/nativeScenario'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { museTest } from '../muse-fixtures'

museTest('draws no compaction notice because the native host refuses to compact', async ({ native }) => {
  const { page, modelScript } = native
  const context = 'COMPACTCONTEXT cedar detail for the compaction probe.'
  const start = await modelScript.queue(nativeTextStep(native, `Noted: ${context}`))
  await sendMessage(page, modelScript.prompt('Record the compaction context.'))
  await modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(page)

  const after = await modelScript.queue(nativeTextStep(native, 'The compact command reached the model as text.'))
  await sendMessage(page, '/compact')
  await modelScript.waitForSteps(after + 1)
  await waitForAgentIdle(page)
  // The refused compaction degrades to an ordinary message: the model read the
  // command, and the earlier context survived it.
  const request = await modelScript.requestAt(after)
  expect(nativeModelContextText(request)).toContain('/compact')
  expect(nativeModelContextText(request)).toContain(context)
  await expect(compactionNoticeRow(page)).toHaveCount(0)
  await page.reload()
  await waitForAgentIdle(page)
  await expect(compactionNoticeRow(page)).toHaveCount(0)
})
