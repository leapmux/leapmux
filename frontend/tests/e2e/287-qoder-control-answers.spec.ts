import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

/**
 * 287 — Qoder CLI control answers.
 *
 * A write raises a banner in Default mode (note 26 keeps read-only commands
 * silent). Typing before the deny turns the button into "Send feedback", which
 * sends the typed text as the denial. The working directory proves the refused
 * call never ran. Qoder's wire answer carries no reason field, so the reason
 * stays on the browser side of the refusal.
 */
qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.QODER

qoderTest.describe('Qoder CLI control answers', () => {
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
    await waitForAgentIdle(page, 180_000)
    expect(existsSync(marker), 'the denied command never ran').toBe(false)
    await expect(assistantBubbles(page).filter({ hasText: 'I stopped at the denial.' }).first()).toBeVisible()
  })
})
