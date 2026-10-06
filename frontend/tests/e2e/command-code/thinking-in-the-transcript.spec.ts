import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { assistantBubbles, bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'

commandCodeTest('shows native thinking and answer rows before and after reload', async ({ authenticatedCommandCodeWorkspace, page, modelScript }) => {
  void authenticatedCommandCodeWorkspace
  await modelScript.queue({ reasoning: 'I inspect the actual native request.', text: 'The native answer completed.' })
  await sendMessage(page, modelScript.prompt('Return the scripted native reasoning and answer.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const inspect = async () => {
    await expect(bandRows(page, 'thought').filter({ hasText: 'I inspect the actual native request.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The native answer completed.' }).first()).toBeVisible()
  }
  await inspect()
  await page.reload()
  await inspect()
})
