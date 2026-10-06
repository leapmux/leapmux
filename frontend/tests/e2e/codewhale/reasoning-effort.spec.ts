import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { CODEWHALE_VISION_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codewhaleTest.describe('Codewhale settings', () => {
  codewhaleTest('sends the chosen effort with the next turn', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)

    // The runtime takes the effort per turn, so the model request is where the
    // choice must arrive.
    await modelScript.queue({ text: 'Done.' })
    await sendMessage(page, modelScript.prompt('Reply with the single word: Done.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const { requests } = await modelScript.status()
    expect(JSON.stringify(requests[0]!.body)).toContain('"reasoning_effort":"high"')
    await page.reload()
    await waitForSettingsHydrated(page)
    const restored = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.CODEWHALE }, 'Reply after restoring high effort.', 'The restored high effort answered.')
    expect(restored.body).toHaveProperty('reasoning_effort', 'high')
  })
})

// The effort belongs to LeapMux, and the next turn sends it. Nothing in the runtime may reset it on a model switch.
codewhaleTest('keeps the chosen effort after a model switch and a reload', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'effort', value: 'high' },
    model: CODEWHALE_VISION_MODEL_ID,
    nativeProof: (request) => {
      expect(request.body).toHaveProperty('model', CODEWHALE_VISION_MODEL_ID)
      expect(request.body).toHaveProperty('reasoning_effort', 'high')
    },
  })
})
