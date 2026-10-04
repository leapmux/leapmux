import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { randomUUID } from 'node:crypto'
import { expect } from '@playwright/test'
import { compactionNoticeRow } from '../helpers/compaction'
import { nativeModelContextText } from '../helpers/nativeScenario'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'

/** The actual ACP path submits the slash command as model text and retains prior context. */
export async function proveNoNativeManualCompaction(page: Page, modelScript: ModelScript): Promise<void> {
  const start = (await modelScript.status()).stepCount
  const marker = randomUUID().replaceAll('-', '')
  const firstPrompt = `Create context before the compact command. ${marker}`
  const firstAnswer = `The earlier context is present. ANSWER${marker}`
  await modelScript.queue({ text: firstAnswer }, { text: 'The slash command reached the model as text.' })
  await sendMessage(page, modelScript.prompt(firstPrompt))
  await modelScript.waitForSteps(start + 1)
  await waitForAgentIdle(page)
  await expect(userBubbles(page).filter({ hasText: 'Create context before the compact command.' }).first()).toBeVisible()
  await expect(page.locator('[data-testid="agent-input-queue"]:visible')).toHaveCount(0)
  await sendMessage(page, '/compact')
  const status = await modelScript.waitForSteps(start + 2)
  const request = status.requests.find(record => record.stepIndex === start + 1)
  if (!request)
    throw new Error('The actual compact command reached no native model request.')
  expect(JSON.stringify(request.body)).toContain('/compact')
  expect(nativeModelContextText(request)).toContain(firstPrompt)
  expect(nativeModelContextText(request)).toContain(firstAnswer)
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'The slash command reached the model as text.' }).first()).toBeVisible()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
}
