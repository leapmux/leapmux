import { expect } from '@playwright/test'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale basic chat', () => {
  codewhaleTest('continues the same thread with a second message', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await modelScript.queue({ text: 'The code word is HALIBUT.' })
    await sendMessage(page, modelScript.prompt('Remember a code word for me.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await modelScript.queue({ text: 'You asked me to remember HALIBUT.' })
    await sendMessage(page, modelScript.prompt('Which code word did you give me?'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The second native request carries the first prompt and answer.
    const { requests } = await modelScript.status()
    expect(requests).toHaveLength(2)
    expect(JSON.stringify(requests[1]!.body)).toContain('Remember a code word for me.')
    expect(JSON.stringify(requests[1]!.body)).toContain('The code word is HALIBUT.')
    await expect(assistantBubbles(page).filter({ hasText: 'You asked me to remember HALIBUT.' })).toBeVisible()
  })
})
