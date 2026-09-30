import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from './copilot-fixtures'
import { expect } from './fixtures'
import { MOCK_MODELS, MOCK_PROVIDER_IDS, REASONIX_ALT_MODEL_ID } from './helpers/mockAgentEnvironment'
import { assistantBubbles, chooseSettingsOption, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsIdle } from './helpers/ui'
import { KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'
import { PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from './zcode-fixtures'

async function exerciseModelSwitch(page: Page, modelScript: ModelScript, optionId: string, wireModel: string): Promise<void> {
  const option = `model-${optionId}`
  await chooseSettingsOption(page, option)
  await waitForSettingsIdle(page)
  await expectSettingsOptionChosen(page, option)

  await modelScript.queue({ text: 'The selected model answered.' })
  await sendMessage(page, modelScript.prompt('Reply once after I switch models.'))
  const status = await modelScript.waitForSteps()
  const request = status.requests.find(item => item.stepIndex === 0)
  expect(request?.body).toMatchObject({ model: wireModel })
  await waitForAgentIdle(page)
  await expect(assistantBubbles(page).filter({ hasText: 'The selected model answered.' }).first()).toBeVisible()

  await page.reload()
  await expectSettingsOptionChosen(page, option)
}

copilotTest.describe('Copilot model', () => {
  copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')
  copilotTest('sends the selected model on the next native request and keeps it after reload', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
    void authenticatedCopilotWorkspace
    await exerciseModelSwitch(page, modelScript, MOCK_MODELS.gooseReasoning, MOCK_MODELS.gooseReasoning)
  })
})

kiloTest.describe('Kilo model', () => {
  kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')
  kiloTest('sends the selected model on the next native request and keeps it after reload', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
    void authenticatedKiloWorkspace
    await exerciseModelSwitch(page, modelScript, `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`, MOCK_MODELS.pi)
  })
})

opencodeTest.describe('OpenCode model', () => {
  opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')
  opencodeTest('sends the selected model on the next native request and keeps it after reload', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    await exerciseModelSwitch(page, modelScript, `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`, MOCK_MODELS.pi)
  })
})

piTest.describe('Pi model', () => {
  piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')
  piTest('sends the selected model on the next native request and keeps it after reload', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    void authenticatedPiWorkspace
    await exerciseModelSwitch(page, modelScript, MOCK_MODELS.zai, MOCK_MODELS.zai)
  })
})

reasonixTest.describe('Reasonix model', () => {
  reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')
  reasonixTest('sends the selected model on the next native request and keeps it after reload', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
    void authenticatedReasonixWorkspace
    await exerciseModelSwitch(page, modelScript, REASONIX_ALT_MODEL_ID, MOCK_MODELS.pi)
  })
})

zcodeTest.describe('ZCode model', () => {
  zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')
  zcodeTest('sends the selected model on the next native request and keeps it after reload', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await exerciseModelSwitch(page, modelScript, `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.pi}`, MOCK_MODELS.pi)
  })
})
