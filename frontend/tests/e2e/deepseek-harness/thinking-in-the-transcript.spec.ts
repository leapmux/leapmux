import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { assistantBubbles, bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'

deepseekHarnessTest('shows distinct native reasoning and answer blocks before and after reload', async ({ deepseekHarnessWorkspace, page, modelScript }) => {
  void deepseekHarnessWorkspace
  await modelScript.queue({ reasoning: 'I inspect the native call result.', text: 'The native answer completed.' })
  await sendMessage(page, modelScript.prompt('Return the scripted native reasoning and answer.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const inspect = async () => {
    await expect(bandRows(page, 'thought').filter({ hasText: 'I inspect the native call result.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The native answer completed.' }).first()).toBeVisible()
  }
  await inspect()
  await page.reload()
  await inspect()
})
