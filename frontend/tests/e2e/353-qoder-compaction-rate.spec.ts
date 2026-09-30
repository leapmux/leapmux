import { compactionNoticeRow, expectCompactionNotice } from './helpers/compaction'
import { rateLimitWindowLabel } from './helpers/rateLimit'
import { assistantBubbles, openAgentInfoCard, sendMessage, visibleOnly, waitForAgentIdle } from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

qoderTest.describe('Qoder CLI compaction and rate limits', () => {
  qoderTest('uses a native manual summary and shows its completed boundary', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    const oldMarker = 'QODER_OLD_CONTEXT_MARKER'
    const summaryMarker = 'QODER_COMPACTED_CONTEXT_MARKER'
    const olderAnswer = Array.from({ length: 3000 }, (_, index) => `${oldMarker} item ${index} records detail ${index * 7}.`).join(' ')
    await modelScript.rule({
      name: 'qoder manual summary',
      when: { body: 'CRITICAL: Respond with TEXT ONLY' },
      respond: {
        text: `<analysis>Keep only the task state.</analysis><summary>${summaryMarker} The prior work recorded the task state and recent decisions.</summary>`,
        usage: { inputTokens: 8000, outputTokens: 40 },
      },
    })
    for (let turn = 0; turn < 4; turn++) {
      // Qoder compares the new history with reported input tokens. The mock's
      // default one-token count makes a real summary look larger than its source.
      await modelScript.queue({
        text: turn === 0 ? olderAnswer : `Recent task answer ${turn}.`,
        usage: { inputTokens: 6000 + turn * 500, outputTokens: turn === 0 ? 5000 : 50 },
      })
      await sendMessage(page, modelScript.prompt(`Record Qoder task turn ${turn}.`))
      await modelScript.waitForSteps(turn + 1)
      await waitForAgentIdle(page)
    }

    await sendMessage(page, '/compact')
    await expect.poll(async () => (await modelScript.status()).ruleMatches['qoder manual summary'] ?? 0).toBeGreaterThan(0)
    await waitForAgentIdle(page)
    await expectCompactionNotice(page)

    await modelScript.queue({ text: 'The compacted task continued.' })
    await sendMessage(page, modelScript.prompt('Continue after the native compaction.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const next = status.requests.find(request => request.stepIndex === 4)
    expect(JSON.stringify(next?.body)).toContain(summaryMarker)
    expect(JSON.stringify(next?.body)).not.toContain(oldMarker)
  })

  qoderTest('does not claim a failed native manual compaction succeeded', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await modelScript.queue({ text: 'Ready to compact.' })
    await sendMessage(page, modelScript.prompt('Reply once, then I will compact.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await modelScript.fallback({ text: 'Earlier work summarized.' })
    await sendMessage(page, '/compact')
    await waitForAgentIdle(page)
    const status = await modelScript.status()
    expect(status.requests.some(request => request.fallback)).toBe(true)
    await expect(visibleOnly(page.getByText('Turn failed', { exact: false })).first()).toBeVisible()
    await expect(compactionNoticeRow(page)).toHaveCount(0)
    await expect(visibleOnly(page.getByText('LeapMux has no display for this row', { exact: true }))).toHaveCount(0)
  })

  qoderTest('does not show a rate-limit window from BYOK model headers', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    const rateLimits = {
      type: 'five_hour',
      status: 'allowed_warning',
      utilization: 0.92,
      resetsAt: Math.floor(Date.now() / 1000) + 3600,
    }
    await modelScript.queue({ text: 'The model response arrived near the limit.', rateLimits })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The model response arrived near the limit.' }).first()).toBeVisible()

    const card = await openAgentInfoCard(page)
    await expect(card).not.toContainText(rateLimitWindowLabel(rateLimits.type))
  })
})
