import { expect } from '@playwright/test'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro basic chat', () => {
  kiroTest('continues the conversation in the same session', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await modelScript.queue({ text: 'First answer.' }, { text: 'Second answer.' })
    await sendMessage(page, modelScript.prompt('Say the first answer.'))
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'First answer.' })).toBeVisible()

    await sendMessage(page, modelScript.prompt('Say the second answer.'))
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Second answer.' })).toBeVisible()
    // Kiro sends the history of the session with each turn.
    const second = JSON.stringify((await modelScript.status()).requests.at(-1)?.body)
    expect(second).toContain('Say the first answer.')
    expect(second).toContain('First answer.')
  })
})
