import type { ModelScript } from './helpers/modelScriptFixture'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest, expect, expectCodewhalePosture } from './codewhale-fixtures'
import { exerciseProviderSteer } from './helpers/providerSteer'
import { bashToolCall } from './helpers/providerToolCalls'
import {
  applyPermissionPreset,
  chooseSettingsOption,
  expectSettingsChip,
  openPlusMenu,
  sendMessage,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'

/** Read the native tool result that the next model request consumes. */
async function expectNativeModeToolResult(modelScript: ModelScript, stepIndex: number, expectedText: string): Promise<void> {
  const status = await modelScript.waitForSteps(stepIndex + 1)
  const request = status.requests.find(record => record.stepIndex === stepIndex)
  expect(request, 'the tool result reached the model').toBeDefined()
  const body = request?.body as { messages?: { role?: string, content?: unknown }[] } | undefined
  const results = (body?.messages ?? []).filter(message => message.role === 'tool')
  expect(results, 'the model consumed a native tool result').not.toHaveLength(0)
  expect(JSON.stringify(results.at(-1)!.content)).toContain(expectedText)
}

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale settings', () => {
  codewhaleTest('steers a queued message into the active turn', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await exerciseProviderSteer(page, modelScript, AgentProvider.CODEWHALE)
  })

  codewhaleTest('applies the mode, the effort and the posture, and keeps them after a reload', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Agent')
    await expectCodewhalePosture(page, 'ask')

    await chooseSettingsOption(page, 'codewhale_mode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await chooseSettingsOption(page, 'effort-high')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'High')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'mode-plan', 'echo "mode-plan-$((40 + 2))"')] },
      { text: 'The Plan turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Check whether Plan mode permits a shell command.'))
    await expectNativeModeToolResult(modelScript, 1, 'not available in Plan mode')
    await waitForAgentIdle(page)

    // Both presets map onto a posture of the runtime: Smart onto its own review
    // rules, Bypass onto full access.
    const menu = await openPlusMenu(page)
    await expect(menu.getByTestId('composer-smart-permissions')).toBeVisible()
    await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
    await page.keyboard.press('Escape')
    await applyPermissionPreset(page, 'bypass')
    await expectCodewhalePosture(page, 'full_access')
    await applyPermissionPreset(page, 'smart')
    await expectCodewhalePosture(page, 'auto_review')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'High')
    await expectCodewhalePosture(page, 'auto_review')

    await chooseSettingsOption(page, 'codewhale_mode-agent')
    await chooseSettingsOption(page, 'permissionMode-ask')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Agent')
    await expectCodewhalePosture(page, 'ask')

    await applyPermissionPreset(page, 'bypass')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'mode-agent', 'echo "mode-agent-$((40 + 2))"')] },
      { text: 'The Agent turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the shell command in Agent mode.'))
    await expectNativeModeToolResult(modelScript, 3, 'mode-agent-42')
    await waitForAgentIdle(page)
    await chooseSettingsOption(page, 'permissionMode-ask')
    await waitForSettingsIdle(page)
    await expectCodewhalePosture(page, 'ask')
  })

  codewhaleTest('Shift+Tab toggles plan mode from the composer', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Agent')

    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'shortcut-plan', 'echo "shortcut-plan-$((40 + 2))"')] },
      { text: 'The Plan shortcut turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Check whether the Plan shortcut permits a shell command.'))
    await expectNativeModeToolResult(modelScript, 1, 'not available in Plan mode')
    await waitForAgentIdle(page)

    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Agent')

    await applyPermissionPreset(page, 'bypass')
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CODEWHALE, 'shortcut-agent', 'echo "shortcut-agent-$((40 + 2))"')] },
      { text: 'The Agent shortcut turn ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the shell command through the Agent shortcut.'))
    await expectNativeModeToolResult(modelScript, 3, 'shortcut-agent-42')
    await waitForAgentIdle(page)
  })

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
  })
})
