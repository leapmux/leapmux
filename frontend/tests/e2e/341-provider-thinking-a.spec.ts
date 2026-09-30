import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { COPILOT_E2E_SKIP_REASON, copilotTest, expect } from './copilot-fixtures'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from './cursor-fixtures'
import { GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { attachCopilotNativeArtifacts } from './helpers/copilotNativeArtifacts'
import { MOCK_MODELS } from './helpers/mockAgentEnvironment'
import { bandRows, chooseSettingsOption, sendMessage, waitForAgentIdle } from './helpers/ui'
import { KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from './zcode-fixtures'

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

copilotTest.describe('Copilot thinking transcript', () => {
  copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')
  copilotTest('keeps a thought before its answer after reload', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }, testInfo) => {
    void authenticatedCopilotWorkspace
    await chooseSettingsOption(page, `model-${MOCK_MODELS.gooseReasoning}`)
    try {
      await proveThinkingRows(page, modelScript)
    }
    catch (error) {
      await attachCopilotNativeArtifacts(leapmuxServer.agentEnv.COPILOT_HOME, testInfo)
      throw error
    }
  })
})

cursorTest.describe('Cursor thinking transcript', () => {
  cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')
  cursorTest('keeps a thought before its answer after reload', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
    void authenticatedCursorWorkspace
    await proveThinkingRows(page, modelScript)
  })
})

kiloTest.describe('Kilo thinking transcript', () => {
  kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')
  kiloTest('keeps a thought before its answer after reload', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
    void authenticatedKiloWorkspace
    await proveThinkingRows(page, modelScript)
  })
})

opencodeTest.describe('OpenCode thinking transcript', () => {
  opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')
  opencodeTest('keeps a thought before its answer after reload', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    await proveThinkingRows(page, modelScript)
  })
})

gooseTest.describe('Goose thinking transcript', () => {
  gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')
  gooseTest('keeps a thought after its streamed answer on reload', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
    void authenticatedGooseWorkspace
    await chooseSettingsOption(page, `model-${MOCK_MODELS.gooseReasoning}`)
    await proveThinkingRows(page, modelScript, 'after')
  })
})

reasonixTest.describe('Reasonix thinking transcript', () => {
  reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')
  reasonixTest('keeps a thought before its answer after reload', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
    void authenticatedReasonixWorkspace
    await proveThinkingRows(page, modelScript)
  })
})

zcodeTest.describe('ZCode thinking transcript', () => {
  zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')
  zcodeTest('keeps a thought before its answer after reload', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await proveThinkingRows(page, modelScript)
  })
})
