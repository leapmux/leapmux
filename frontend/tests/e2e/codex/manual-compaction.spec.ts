import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { expectCompactionNotice } from '../helpers/compaction'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

const OLD_CONTEXT_MARKER = 'LEAPMUXOLDCONTEXTCODEXRIVER'

const SUMMARY_MARKER = 'Earlier Codex work summarized.'

codexTest.describe('Codex compaction notice', () => {
  // `/compact` starts a native compaction. The mock answers the summarizer
  // from a rule. The CLI's context-compaction item supplies the notice.
  codexTest('a manual compaction draws the notice and removes old context', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await modelScript.rule({
      name: 'compaction-summarizer',
      // Every pattern in this list must match the native summary request.
      when: { user: ['compact', 'summary'] },
      respond: { text: SUMMARY_MARKER },
    })
    await modelScript.queue(
      { text: 'The first Codex turn ended.' },
      { text: 'The second Codex turn ended.' },
      { text: 'The third Codex turn ended.' },
    )
    await sendMessage(page, modelScript.prompt(`Keep ${OLD_CONTEXT_MARKER} in the older context.`))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page)
    await sendMessage(page, modelScript.prompt('Add a newer turn before compaction.'))
    const before = await modelScript.waitForSteps(2)
    expect(JSON.stringify(before.requests.find(request => request.stepIndex === 1)?.body)).toContain(OLD_CONTEXT_MARKER)
    await waitForAgentIdle(page)
    await sendMessage(page, modelScript.prompt('Add the current turn before compaction.'))
    await modelScript.waitForSteps(3)
    await waitForAgentIdle(page)

    await sendMessage(page, '/compact')
    await waitForAgentIdle(page)

    await expectCompactionNotice(page)
    const compacted = await modelScript.status()
    expect(compacted.ruleMatches['compaction-summarizer']).toBeGreaterThan(0)

    await modelScript.queue({ text: 'The compacted Codex session continued.' })
    await sendMessage(page, modelScript.prompt('Continue after manual compaction.'))
    const continued = await modelScript.waitForSteps(4)
    const nextRequest = JSON.stringify(continued.requests.find(request => request.stepIndex === 3)?.body)
    expect(nextRequest).toContain(SUMMARY_MARKER)
    expect(nextRequest).not.toContain('The first Codex turn ended.')
    expect(nextRequest).not.toContain('The second Codex turn ended.')
    await waitForAgentIdle(page)
  })
})
