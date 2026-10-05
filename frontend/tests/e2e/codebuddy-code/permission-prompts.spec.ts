import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from '../codebuddy-fixtures'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code control requests', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('raises a banner for a tool call and runs it once allowed', async ({ askingCodebuddyWorkspace, page, modelScript }) => {
    const output = join(askingCodebuddyWorkspace.workingDir, 'codebuddy-permission-allowed.txt')
    const command = 'printf CODEBUDDY_ALLOWED > ./codebuddy-permission-allowed.txt'
    const call = bashToolCall(AgentProvider.CODEBUDDY, 'call-1', command)
    await modelScript.queue({ toolCalls: [call] })
    await modelScript.queue({ text: 'The command ran.' })
    await sendMessage(page, modelScript.prompt('Run the scripted write command.'))

    // Wait for the native model request before you check the banner.
    // Agent startup can exceed the banner assertion deadline.
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText(command)
    expect(existsSync(output)).toBe(false)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(readFileSync(output, 'utf8')).toBe('CODEBUDDY_ALLOWED')
  })

  codebuddyTest('does not run a denied write command', async ({ askingCodebuddyWorkspace, page, modelScript }) => {
    const output = join(askingCodebuddyWorkspace.workingDir, 'codebuddy-permission-denied.txt')
    const command = 'printf CODEBUDDY_DENIED > ./codebuddy-permission-denied.txt'
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.CODEBUDDY, 'denied-call', command)] })
    await modelScript.fallback({ text: 'The denied call ended.' })
    await sendMessage(page, modelScript.prompt('Try the scripted write command.'))
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText(command)
    expect(existsSync(output)).toBe(false)
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(existsSync(output)).toBe(false)
  })
})

codebuddyTest.describe('CodeBuddy Code control answers', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.CODEBUDDY

  function laterRequests(status: { requests: { stepIndex?: number, body: unknown }[] }, from: number): string {
    return status.requests
      .filter(request => (request.stepIndex ?? -1) >= from)
      .map(request => JSON.stringify(request.body))
      .join('\n')
  }

  codebuddyTest('a denied command does not run, and the reason reaches the model', async ({ askingCodebuddyWorkspace, page, modelScript }) => {
    const { workingDir } = askingCodebuddyWorkspace
    const marker = join(workingDir, 'codebuddy-denied-marker')
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'deny-call', 'touch codebuddy-denied-marker')] },
      { text: 'I stopped at the denial.' },
    )
    await sendMessage(page, modelScript.prompt('Create the marker file.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('touch codebuddy-denied-marker')
    // Typing turns the deny button into "Send feedback", which sends the typed
    // text as the denial reason.
    await page.locator('[data-testid="composer-editor"] .ProseMirror').click()
    await page.keyboard.type('the probe is not wanted here', { delay: 50 })
    const deny = page.getByTestId('control-deny-btn')
    await expect(deny).toHaveText('Send feedback')
    await deny.click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(existsSync(marker), 'the denied command never ran').toBe(false)
    expect(laterRequests(status, 1)).toContain('the probe is not wanted here')
    await expect(assistantBubbles(page).filter({ hasText: 'I stopped at the denial.' }).first()).toBeVisible()
  })
})
