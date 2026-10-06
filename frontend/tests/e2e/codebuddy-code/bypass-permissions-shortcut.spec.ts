import { CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chooseSettingsOption, closeComposerMenus, expectSettingsChip, openPlusMenu, openSettingsMenu, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest('offers Bypass but no Smart shortcut and runs a native tool after Bypass', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.Default}`)
    await waitForSettingsIdle(page)
    const defaultModes = await openSettingsMenu(page, 'permissionMode')
    await expect(defaultModes.locator(`[data-testid="permissionMode-${CODEBUDDY_MODE.Default}"] input[type="radio"]`)).toBeChecked()
    await closeComposerMenus(page)

    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await closeComposerMenus(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Bypass Permissions')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEBUDDY, 'bypass-proof', 'echo "codebuddy-bypass-$((40 + 2))"')] },
      { text: 'The bypass command ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command after Bypass.'))
    await modelScript.waitForSteps(1)
    await expect(page.getByTestId('control-banner').filter({ visible: true })).toHaveCount(0)
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const messages = (status.requests.find(request => request.stepIndex === 1)?.body as { messages?: { role?: string, content?: unknown }[] } | undefined)?.messages ?? []
    const result = messages.findLast(message => message.role === 'tool')?.content
    expect(JSON.stringify(result)).toContain('codebuddy-bypass-42')
  })
})
