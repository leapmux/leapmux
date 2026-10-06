import { DIRAC_AGENT, diracTest, expect } from '../dirac-fixtures'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectSettingsChip, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { nativeContext } from './scenarios'

diracTest.describe('Dirac settings apply', () => {
  diracTest('switches the mode and the effort, and keeps them after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, DIRAC_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Act')

    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await chooseSettingsOption(page, 'reasoning_effort-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'High')

    await chooseSettingsOption(page, 'permissionMode-act')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Act')
  })

  diracTest('sends the selected effort in the next native request', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, DIRAC_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Act')

    await chooseSettingsOption(page, 'reasoning_effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')

    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-effort-answer', 'complete', 'Dirac answered at low effort.')] })
    await sendMessage(page, modelScript.prompt('Reply once after the effort switch.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ reasoning_effort: 'low' })

    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'reasoning_effort',
      value: 'low',
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ reasoning_effort: 'low' })
      },
    })
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Low')
  })
})
