import { expectCompactionNotice } from './helpers/compaction'
import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from './ohmypi-fixtures'

ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest.describe('Oh My Pi compaction notice', () => {
  ohMyPiTest('draws the native notice after a manual compaction', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    const summaryMarker = 'OH_MY_PI_COMPACT_SUMMARY_MARKER'
    await modelScript.rule({
      name: 'compaction summarizer',
      when: { body: 'You MUST summarize the conversation above into a structured handoff summary' },
      respond: { text: `${summaryMarker} Earlier work summarized.` },
    }, {
      name: 'compaction short summary',
      when: { system: 'Summarize user.AI coding-assistant conversations' },
      respond: { text: `${summaryMarker} I kept the recent turn.` },
    })
    const olderAnswer = Array.from({ length: 300 }, (_, index) => `OLDER_CONTEXT item ${index}: detail ${index * 7}.`).join(' ')
    await modelScript.queue({ text: olderAnswer })
    await sendMessage(page, modelScript.prompt('Record the older material.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'OLDER_CONTEXT item 299' })).toBeVisible()

    await modelScript.queue({ text: 'Ready to compact.' })
    await sendMessage(page, modelScript.prompt('Record a newer turn.'))
    const priorStatus = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const newerRequest = priorStatus.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(newerRequest?.body).includes('OLDER_CONTEXT item 299')).toBe(true)

    await sendMessage(page, '/compact')
    await expectCompactionNotice(page)
    const compactedStatus = await modelScript.status()
    expect(compactedStatus.ruleMatches['compaction summarizer']).toBeGreaterThan(0)
    expect(compactedStatus.ruleMatches['compaction short summary']).toBeGreaterThan(0)
    await waitForAgentIdle(page)

    await modelScript.queue({ text: 'The session continued.' })
    await sendMessage(page, modelScript.prompt('Continue after compaction.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const next = status.requests.find(request => request.stepIndex === 2)
    const nextBody = JSON.stringify(next?.body)
    expect(nextBody).toContain(summaryMarker)
    expect(nextBody).not.toContain('OLDER_CONTEXT')
  })
})
