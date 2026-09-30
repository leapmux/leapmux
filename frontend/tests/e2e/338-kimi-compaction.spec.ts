import { expectCompactionNotice } from './helpers/compaction'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, kimiTest } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest.describe('Kimi Code compaction notice', () => {
  kimiTest('keeps the native summary and removes old context after manual compaction', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    const oldMarker = 'KIMI_OLD_CONTEXT_MARKER'
    const summaryMarker = 'KIMI_COMPACT_SUMMARY_MARKER'
    await modelScript.rule({
      name: 'compaction summarizer',
      when: { user: 'You are about to run out of context' },
      respond: { text: `${summaryMarker} Preserve the current task.` },
    })
    const olderAnswer = Array.from({ length: 300 }, (_, index) => `${oldMarker} item ${index}: detail ${index * 7}.`).join(' ')
    for (let turn = 0; turn < 4; turn++) {
      await modelScript.queue({ text: turn === 0 ? olderAnswer : `Recent task answer ${turn}.` })
      await sendMessage(page, modelScript.prompt(`Record Kimi task turn ${turn}.`))
      const prior = await modelScript.waitForSteps(turn + 1)
      await waitForAgentIdle(page)
      if (turn === 3)
        expect(JSON.stringify(prior.requests.find(request => request.stepIndex === turn)?.body)).toContain(oldMarker)
    }

    await sendMessage(page, '/compact')
    await expectCompactionNotice(page)
    const status = await modelScript.status()
    expect(status.ruleMatches['compaction summarizer']).toBeGreaterThan(0)
    await waitForAgentIdle(page)

    await modelScript.queue({ text: 'The compacted Kimi session continued.' })
    await sendMessage(page, modelScript.prompt('Continue after manual compaction.'))
    const continued = await modelScript.waitForSteps(5)
    await waitForAgentIdle(page)
    const nextBody = JSON.stringify(continued.requests.find(request => request.stepIndex === 4)?.body)
    expect(nextBody).toContain(summaryMarker)
    expect(nextBody).not.toContain(oldMarker)
  })
})
