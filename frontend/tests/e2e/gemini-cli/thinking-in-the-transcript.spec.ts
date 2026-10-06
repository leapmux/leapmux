import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { assistantBubbles, bandRows, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

geminiTest('preserves the native model thought in its own transcript row after reload', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await context.modelScript.queue({ reasoning: 'I inspect the native numbers first.', text: 'The computed answer is 42.' })
  await sendMessage(page, context.modelScript.prompt('Show the scripted thought and answer.'))
  await context.modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const thought = bandRows(page, 'thought').filter({ hasText: 'I inspect the native numbers first.' }).first()
  await expect(thought).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: 'The computed answer is 42.' })).toBeVisible()
  await page.reload()
  await expect(thought).toBeVisible()
})
