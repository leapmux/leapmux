import { expect } from '@playwright/test'
import { compactionNoticeRow } from '../helpers/compaction'
import { exerciseManualCompaction, MANUAL_COMPACTION_SUMMARY } from '../helpers/manualCompaction'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest('preserves the native completed compaction notice after a verified summary and reload', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
  void authenticatedOhMyPiWorkspace
  await modelScript.rule({
    name: 'native-notice-short-summary',
    when: { system: 'Summarize user.AI coding-assistant conversations' },
    respond: { text: MANUAL_COMPACTION_SUMMARY },
  })
  await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'You MUST summarize the conversation above into a structured handoff summary' })
  const notice = compactionNoticeRow(page)
  await expect(notice).toBeVisible()
  expect((await modelScript.status()).ruleMatches['native-notice-short-summary']).toBeGreaterThan(0)
  await page.reload()
  await expect(notice).toBeVisible()
})
