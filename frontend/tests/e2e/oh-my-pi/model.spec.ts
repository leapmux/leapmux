import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { OH_MY_PI_ALT_MODEL_ID, OH_MY_PI_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { OH_MY_PI_E2E_SKIP_REASON, ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The selected model must reach an actual native request. The setting must survive a page reload.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.skip(!!OH_MY_PI_E2E_SKIP_REASON, OH_MY_PI_E2E_SKIP_REASON || '')

ohMyPiTest('switches the model for the next native request', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
  void authenticatedOhMyPiWorkspace
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, `model-${OH_MY_PI_ALT_MODEL_ID}`)
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'GLM-5.3 Alternate')

  await modelScript.queue({ text: 'The alternate model answered.' })
  await sendMessage(page, modelScript.prompt('Reply once with the alternate model.'))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  const body = JSON.stringify(status.requests.find(request => request.stepIndex === 0)?.body)
  expect(body.includes(`"model":"${OH_MY_PI_ALT_MODEL_WIRE_ID}"`)).toBe(true)

  await page.reload()
  await expectSettingsChip(page, 'GLM-5.3 Alternate')
  const restored = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.OH_MY_PI }, 'Reply after restoring the selected model.', 'The restored model answered.')
  expect(restored.body).toHaveProperty('model', OH_MY_PI_ALT_MODEL_WIRE_ID)
})
