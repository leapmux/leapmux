import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from './droid-fixtures'
import { expectCompactionNotice } from './helpers/compaction'
import { rateLimitWindowLabel } from './helpers/rateLimit'
import { assistantBubbles, messageBubbles, openAgentInfoCard, sendMessage, waitForAgentIdle } from './helpers/ui'

droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

droidTest.describe('Factory Droid compaction', () => {
  droidTest('shows and keeps the native manual compaction notice', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    const oldMarker = 'DROID_OLD_CONTEXT_MARKER'
    const summaryMarker = 'DROID_COMPACT_SUMMARY_MARKER'
    await modelScript.rule(DROID_TITLE_RULE)
    const olderAnswer = Array.from({ length: 300 }, (_, index) => `${oldMarker} item ${index}: detail ${index * 7}.`).join(' ')
    for (let turn = 0; turn < 4; turn++) {
      await modelScript.queue({ text: turn === 0 ? olderAnswer : `Recent task answer ${turn}.` })
      await sendMessage(page, modelScript.prompt(`Record Droid task turn ${turn}.`))
      const prior = await modelScript.waitForSteps(turn + 1)
      await waitForAgentIdle(page)
      if (turn === 3)
        expect(JSON.stringify(prior.requests.find(request => request.stepIndex === turn)?.body)).toContain(oldMarker)
    }
    await expect(assistantBubbles(page).filter({ hasText: 'Recent task answer 3.' })).toBeVisible()

    await modelScript.fallback({ text: `${summaryMarker} Preserve the current task.` })
    await sendMessage(page, '/compact')
    await expectCompactionNotice(page)
    await waitForAgentIdle(page)
    expect((await modelScript.status()).requests.some(request => request.fallback)).toBe(true)
    await expect(messageBubbles(page).filter({ hasText: 'settings_updated' })).toHaveCount(0)
    await expect(messageBubbles(page).filter({ hasText: '"type":"session_compacted"' })).toHaveCount(0)
    await page.reload()
    await expectCompactionNotice(page)
    await expect(messageBubbles(page).filter({ hasText: 'settings_updated' })).toHaveCount(0)

    await modelScript.queue({ text: 'The compacted Droid session continued.' })
    await sendMessage(page, modelScript.prompt('Continue after manual compaction.'))
    const continued = await modelScript.waitForSteps(5)
    await waitForAgentIdle(page)
    const nextBody = JSON.stringify(continued.requests.find(request => request.stepIndex === 4)?.body)
    expect(nextBody).toContain(summaryMarker)
    expect(nextBody).not.toContain(oldMarker)
  })
})

droidTest.describe('Factory Droid rate-limit state', () => {
  droidTest('does not invent a rate window after a native 429 retry', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      { error: { status: 429, code: 'rate_limit_exceeded', message: 'The model limit was reached.' } },
      { text: 'The retry completed.' },
    )
    await sendMessage(page, modelScript.prompt('Reply after the model retry.'))
    const status = await modelScript.waitForSteps()
    expect(status.requests.filter(request => request.stepIndex !== undefined)).toHaveLength(2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The retry completed.' }).first()).toBeVisible()

    const info = await openAgentInfoCard(page)
    await expect(info).not.toContainText(rateLimitWindowLabel('five_hour'))
  })
})
