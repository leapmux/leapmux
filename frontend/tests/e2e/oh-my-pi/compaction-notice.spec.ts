import { expect } from '@playwright/test'
import { compactionNoticeRow } from '../helpers/compaction'
import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('preserves the native completed compaction notice after a verified summary and reload', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
  void authenticatedOhMyPiWorkspace
  // omp sends two summary requests with one system prompt: the handoff summary,
  // then a short summary that ends with its own instruction. The rule takes the
  // short summary alone. The handoff summary reaches the fallback that
  // exerciseManualCompaction proves.
  await modelScript.rule({
    name: 'native-notice-short-summary',
    when: { system: 'Summarize user.AI coding-assistant conversations', user: 'Summarize conversation changes as a pull request description' },
    respond: { text: 'I recorded the older context, a newer note, and the current note.' },
  })
  await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'You MUST summarize the conversation above into a structured handoff summary' })
  const notice = compactionNoticeRow(page)
  await expect(notice).toBeVisible()
  expect((await modelScript.status()).ruleMatches['native-notice-short-summary'], 'omp asks for one short summary for each compaction').toBe(1)
  await page.reload()
  await expect(notice).toBeVisible()
})
