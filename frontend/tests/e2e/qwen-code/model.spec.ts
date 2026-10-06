import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { QWEN_ALT_MODEL_ID, QWEN_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, expectSettingsOptionChosen, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { openQwenAgent, qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code settings and goal', () => {
  qwenTest('switches the model for the next native request', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${QWEN_ALT_MODEL_ID}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${QWEN_ALT_MODEL_ID}`)

    await modelScript.queue({ text: 'The alternate model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once with the alternate model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const body = JSON.stringify(status.requests.find(request => request.stepIndex === 0)?.body)
    expect(body.includes(`"model":"${QWEN_ALT_MODEL_WIRE_ID}"`)).toBe(true)

    await page.reload()
    await expectSettingsOptionChosen(page, `model-${QWEN_ALT_MODEL_ID}`)
    const restored = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.QWEN_CODE }, 'Reply after restoring the selected model.', 'The restored model answered.')
    expect(restored.body).toHaveProperty('model', QWEN_ALT_MODEL_WIRE_ID)
  })
})
