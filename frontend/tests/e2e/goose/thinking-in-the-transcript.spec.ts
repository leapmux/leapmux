import type { Page } from '@playwright/test'
import type { ModelScript } from '../helpers/modelScriptFixture'
import { expect } from '@playwright/test'
import { gooseTest } from '../goose-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { bandRows, chooseSettingsOption, sendMessage, waitForAgentIdle } from '../helpers/ui'

const REASONING = 'THINKING_MATRIX_PROBE: I checked the answer first.'

const ANSWER = 'The answer is THINKING_MATRIX_DONE.'

async function proveThinkingRows(page: Page, modelScript: ModelScript, order: 'before' | 'after' = 'before'): Promise<void> {
  await modelScript.queue({ reasoning: REASONING, text: ANSWER })
  await sendMessage(page, modelScript.prompt('Reason once, then answer.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  const assertRows = async () => {
    await expect(bandRows(page, 'thought').filter({ hasText: REASONING }).first()).toBeVisible()
    await expect(bandRows(page, 'text').filter({ hasText: ANSWER }).first()).toBeVisible()
    const rows = await bandRows(page).allTextContents()
    const thought = rows.findIndex(text => text.includes(REASONING))
    const answer = rows.findIndex(text => text.includes(ANSWER))
    expect(thought).toBeGreaterThanOrEqual(0)
    expect(answer).toBeGreaterThanOrEqual(0)
    if (order === 'before')
      expect(thought).toBeLessThan(answer)
    else
      expect(thought).toBeGreaterThan(answer)
  }
  await assertRows()
  await page.reload()
  await assertRows()
}

gooseTest('keeps a thought after its streamed answer on reload', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
  void authenticatedGooseWorkspace
  await chooseSettingsOption(page, `model-${MOCK_MODELS.gooseReasoning}`)
  await proveThinkingRows(page, modelScript, 'after')
})
