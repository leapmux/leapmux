import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import { expectCompactionNotice } from './helpers/compaction'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

const OLD_CONTEXT_MARKER = 'LEAPMUXOLDCONTEXTCOPILOTRIVER'
const SUMMARY_MARKER = 'Summary: the earlier Copilot turns established the topic.'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest.describe('Copilot compaction notice', () => {
  copilotTest('draws and keeps a native manual compaction notice', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
    void authenticatedCopilotWorkspace
    await modelScript.queue(
      { text: 'The first Copilot turn ended.' },
      { text: 'The second Copilot turn ended.' },
      { text: 'The third Copilot turn ended.' },
      { text: SUMMARY_MARKER },
    )
    await sendMessage(page, modelScript.prompt(`Keep ${OLD_CONTEXT_MARKER} in the older context.`))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page)
    await sendMessage(page, modelScript.prompt('Add another turn before compaction.'))
    const before = await modelScript.waitForSteps(2)
    expect(JSON.stringify(before.requests.find(request => request.stepIndex === 1)?.body)).toContain(OLD_CONTEXT_MARKER)
    await waitForAgentIdle(page)
    await sendMessage(page, modelScript.prompt('Add the current turn before compaction.'))
    await modelScript.waitForSteps(3)
    await waitForAgentIdle(page)

    await sendMessage(page, '/compact')
    await expectCompactionNotice(page)
    await modelScript.waitForSteps(4)
    await waitForAgentIdle(page)
    await page.reload()
    await expectCompactionNotice(page)
    await modelScript.queue({ text: 'The compacted Copilot session continued.' })
    await sendMessage(page, modelScript.prompt('Continue after compaction.'))
    const status = await modelScript.waitForSteps(5)
    const nextRequest = JSON.stringify(status.requests.find(request => request.stepIndex === 4)?.body)
    expect(nextRequest).toContain(SUMMARY_MARKER)
    expect(nextRequest).not.toContain('The first Copilot turn ended.')
    expect(nextRequest).not.toContain('The second Copilot turn ended.')
    await waitForAgentIdle(page)
  })
})
