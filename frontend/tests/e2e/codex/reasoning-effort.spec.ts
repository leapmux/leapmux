import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseModelSwitchKeepsOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codexTest.describe('applies Codex session settings', () => {
  codexTest('sends the selected effort and keeps it after a reload', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, /low/i)

    await modelScript.queue({ text: 'Codex answered at low effort.' })
    await sendMessage(page, modelScript.prompt('Reply once after the effort switch.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ reasoning: { effort: 'low' } })

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, /low/i)
    await modelScript.queue({ text: 'The restored effort reached the next Codex turn.' })
    await sendMessage(page, modelScript.prompt('Reply once after restoring the effort.'))
    const restored = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    expect(restored.requests.find(request => request.stepIndex === 1)?.body).toMatchObject({ reasoning: { effort: 'low' } })
  })
})

// Codex states the model and the effort again in each turn, so both must hold after a switch.
codexTest('keeps the chosen effort after a model switch and a reload', async ({ authenticatedCodexWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodexWorkspace.workspaceId, provider: AgentProvider.CODEX }
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'effort', value: 'low' },
    model: 'gpt-5.6-sol',
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: 'gpt-5.6-sol', reasoning: { effort: 'low' } })
    },
  })
})
