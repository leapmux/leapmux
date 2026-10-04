import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { GROK_ALT_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest.describe('Grok Build settings, folder trust and MCP forms', () => {
  grokTest('switches the model for the next native request', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${GROK_ALT_MODEL_ID}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${GROK_ALT_MODEL_ID}`)

    await modelScript.queue({ text: 'The alternate model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once with the alternate model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const body = JSON.stringify(status.requests.find(request => request.stepIndex === 0)?.body)
    expect(body.includes(`"model":"${GROK_ALT_MODEL_ID}"`)).toBe(true)

    await page.reload()
    await expectSettingsOptionChosen(page, `model-${GROK_ALT_MODEL_ID}`)
    const restored = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.GROK_BUILD }, 'Reply after restoring the selected model.', 'The restored model answered.')
    expect(restored.body).toHaveProperty('model', GROK_ALT_MODEL_ID)
  })
})
