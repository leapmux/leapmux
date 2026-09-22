import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from './helpers/providerToolCalls'
import { applyPermissionPreset, ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, messageContents, openPlusMenu, openSettingsMenu, sendMessage, settingsBar, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from './helpers/ui'
import { expect, ZCODE_E2E_SKIP_REASON, zcodeTest } from './zcode-fixtures'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

/** The command the permission tests script. It never runs: the banner stops it. */
const RISKY_COMMAND = `rm -${'rf'} /tmp/zcode-e2e-must-not-exist`

zcodeTest.describe('uses ZCode for basic chat', () => {
  zcodeTest('opens, sends a prompt, and receives a response', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)
  })

  zcodeTest('assistant response appears in a chat bubble', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await modelScript.queue({ text: 'hello world' })
    await sendMessage(page, modelScript.prompt('Say hello world'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(assistantBubbles(page)).not.toHaveCount(0)
    // expectAssistantAnswer, not lastAssistantBubble: a turn-end divider is an
    // agent-role bubble too, so the LAST one is the divider whenever it lands
    // after the reply.
    await expectAssistantAnswer(page, { answer: /hello/i })
  })
})

zcodeTest.describe('uses ZCode for tool execution', () => {
  zcodeTest('a bash command renders as a tool card with its output', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.ZCODE, 'echo-call', 'echo "zcode-test-output"')] },
      { text: 'The command printed zcode-test-output.' },
    )
    await sendMessage(page, modelScript.prompt('Run the bash command: echo "zcode-test-output" and show me the output.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    const joined = (await messageContents(page).allTextContents()).join(' ')
    expect(joined).toContain('zcode-test-output')
  })
})

zcodeTest.describe('handles ZCode permission prompts', () => {
  // Build is the default and asks before a risky action. A destructive command
  // is the shape that produces a permission banner rather than running silently.
  zcodeTest('a risky command produces a permission banner that can be denied', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.ZCODE, 'risky-call', RISKY_COMMAND)] })
    // The denial ends the turn, so the agent asks for nothing more.
    modelScript.allowUnconsumed('a denied tool call may end the turn without another model request')
    await modelScript.queue({ text: 'I stopped at the confirmation.' })
    await sendMessage(page, modelScript.prompt('Run this exact bash command and do not skip the confirmation.'))

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Bash')
    // Reject lives in the composer footer, not inside the banner slot --
    // ControlRequestContent and ControlRequestActions render in different
    // places, the same split every other provider's e2e already follows.
    const deny = page.getByTestId('control-deny-btn')
    await expect(deny).toBeVisible()
    await deny.click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
  })

  // The banner's permission pills are the same settings change the composer
  // menu's bypass shortcut makes, applied when the request is allowed.
  zcodeTest('the permission banner applies the selected bypass pill on allow', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
    void authenticatedZCodeWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.ZCODE, 'risky-call', RISKY_COMMAND)] },
      { text: 'The command ran.' },
    )
    // The test asserts the settings chip, not the turn, and the allowed command
    // may end the turn before the agent asks again.
    modelScript.allowUnconsumed('the assertion is the permission chip, not a second model turn')
    await sendMessage(page, modelScript.prompt('Run this exact bash command and do not skip the confirmation.'))

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Bash')
    const pills = page.getByRole('radiogroup', { name: 'Permissions' })
    await expect(pills.getByRole('radio', { name: 'Unchanged' })).toBeChecked()
    const bypass = pills.getByRole('radio', { name: 'Bypass' })
    await bypass.click()
    await expect(bypass).toBeChecked()

    await page.getByTestId('control-allow-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    // The same chip the composer menu's bypass shortcut lands on.
    await expectSettingsChip(page, 'Yolo')
  })
})

zcodeTest.describe('changes ZCode modes', () => {
  zcodeTest('offers only the bypass permission shortcut', async ({ authenticatedZCodeWorkspace, page }) => {
    void authenticatedZCodeWorkspace
    await waitForSettingsHydrated(page)
    const menu = await openPlusMenu(page)
    // ZCode declares no Smart preset, so only the bypass shortcut is drawn.
    await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Yolo')
  })

  zcodeTest('the mode chip starts on Build and can switch to Plan and Yolo', async ({ authenticatedZCodeWorkspace, page }) => {
    void authenticatedZCodeWorkspace
    await expect(settingsBar(page)).toBeVisible()
    await expectSettingsChip(page, 'Build')

    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan')

    await chooseSettingsOption(page, 'permissionMode-yolo')
    await expectSettingsChip(page, 'Yolo')

    await chooseSettingsOption(page, 'permissionMode-build')
    await expectSettingsChip(page, 'Build')
  })

  zcodeTest('auto is not offered, because the shipped app-server does not implement it', async ({ authenticatedZCodeWorkspace, page }) => {
    void authenticatedZCodeWorkspace
    const menu = await openSettingsMenu(page, 'permissionMode')
    await expect(menu.locator('[data-testid="permissionMode-auto"]')).toHaveCount(0)
    await expect(menu.locator('[data-testid="permissionMode-plan"]')).toBeVisible()
    await expect(menu.locator('[data-testid="permissionMode-build"]')).toBeVisible()
    await expect(menu.locator('[data-testid="permissionMode-edit"]')).toBeVisible()
    await expect(menu.locator('[data-testid="permissionMode-yolo"]')).toBeVisible()
    await page.keyboard.press('Escape')
  })
})
