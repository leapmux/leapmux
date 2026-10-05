import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { expect, expectQoderModeChip, QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI control requests', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('raises a banner for a tool call and runs it once allowed', async ({ askingQoderWorkspace, page, modelScript }) => {
    const output = join(askingQoderWorkspace.workingDir, 'qoder-control-probe.txt')
    const command = 'printf hi > ./qoder-control-probe.txt'
    const call = bashToolCall(AgentProvider.QODER, 'call-1', command)
    await modelScript.queue({ toolCalls: [call] })
    await modelScript.queue({ text: 'The command ran.' })
    await sendMessage(page, modelScript.prompt(`Run ${command}.`))

    // Wait for the native model request before you check the banner.
    // Agent startup can exceed the banner assertion deadline.
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('qoder-control-probe.txt')
    expect(existsSync(output)).toBe(false)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(readFileSync(output, 'utf8')).toBe('hi')
  })

  qoderTest('keeps a denied write out of the workspace', async ({ askingQoderWorkspace, page, modelScript }) => {
    const output = join(askingQoderWorkspace.workingDir, 'qoder-control-denied.txt')
    const command = 'printf denied > ./qoder-control-denied.txt'
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.QODER, 'denied-call', command)] })
    await modelScript.fallback({ text: 'The denied call ended.' })
    await sendMessage(page, modelScript.prompt(`Try ${command}.`))
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('qoder-control-denied.txt')
    expect(existsSync(output)).toBe(false)
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(existsSync(output)).toBe(false)
  })
})

qoderTest.describe('Qoder CLI settings', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('runs a read-only shell command with no banner in Default mode', async ({ askingQoderWorkspace, page, modelScript }) => {
    const { workingDir } = askingQoderWorkspace
    await waitForSettingsHydrated(page)
    await expectQoderModeChip(page, 'Default')

    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.QODER, 'read-only', 'pwd')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run pwd.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    const followUp = status.requests.find(record => record.stepIndex === 1)
    expect(followUp?.body).toMatchObject({
      messages: expect.arrayContaining([
        expect.objectContaining({ role: 'tool', tool_call_id: 'read-only', content: workingDir }),
      ]),
    })
    await expect(page.locator('[data-testid="chat-container"]:visible').getByText(workingDir, { exact: false }).filter({ visible: true }).first()).toBeVisible()
  })
})

qoderTest.describe('Qoder CLI control answers', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.QODER

  qoderTest('a denied write does not run, and the typed reason dismisses the banner', async ({ askingQoderWorkspace, page, modelScript }) => {
    const { workingDir } = askingQoderWorkspace
    const marker = join(workingDir, 'qoder-denied-marker')
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'deny-call', 'printf x > qoder-denied-marker')] },
      { text: 'I stopped at the denial.' },
    )
    await sendMessage(page, modelScript.prompt('Create the marker file.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('qoder-denied-marker')
    await page.locator('[data-testid="composer-editor"] .ProseMirror').click()
    await page.keyboard.type('the probe is not wanted here', { delay: 50 })
    const deny = page.getByTestId('control-deny-btn')
    await expect(deny).toHaveText('Send feedback')
    await deny.click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(existsSync(marker), 'the denied command never ran').toBe(false)
    await expect(assistantBubbles(page).filter({ hasText: 'I stopped at the denial.' }).first()).toBeVisible()
  })
})
