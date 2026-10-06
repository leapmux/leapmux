import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

/**
 * The selected effort must reach an actual native model request.
 *
 * Cline exposes native reasoning effort for catalog models that support it.
 * The configured custom model exposes no effort ladder.
 */
clineTest.describe('Cline settings', () => {
  clineTest('applies reasoning effort to the native model request', async ({ askingClineWorkspace, page, modelScript }) => {
    void askingClineWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'model-deepseek-v4-pro')
    await waitForSettingsIdle(page)

    await modelScript.queue({ text: 'Default effort answered.' })
    await sendMessage(page, modelScript.prompt('Reply once at default effort.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')

    await modelScript.queue({ text: 'High effort answered.' })
    await sendMessage(page, modelScript.prompt('Reply once at high effort.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const first = status.requests.find(request => request.stepIndex === 0)?.body
    const second = status.requests.find(request => request.stepIndex === 1)?.body
    if (!first || typeof first !== 'object' || !second || typeof second !== 'object')
      throw new Error('both Cline model requests must be recorded')
    expect((first as { reasoning_effort?: unknown }).reasoning_effort).toBeUndefined()
    expect((second as { reasoning_effort?: string }).reasoning_effort).toBe('high')
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'High')
    const restored = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.CLINE }, 'Reply after restoring high effort.', 'The restored high effort answered.')
    expect(restored.body).toHaveProperty('reasoning_effort', 'high')
    expect(restored.body).toHaveProperty('model', 'deepseek-v4-pro')
  })
})

clineTest.describe('Cline model switch', () => {
  // Both catalog models offer high. Cline merges each field of a connection update alone, so a model switch keeps the effort.
  clineTest('keeps the chosen effort after a model switch and a reload', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
    await exerciseModelSwitchKeepsOption(context, {
      prepare: async () => {
        await waitForSettingsHydrated(page)
        await chooseSettingsOption(page, 'model-deepseek-v4-pro')
        await waitForSettingsIdle(page)
      },
      kept: { groupId: 'effort', value: 'high' },
      model: 'deepseek-flash',
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: 'deepseek-flash', reasoning_effort: 'high' })
      },
    })
  })
})
