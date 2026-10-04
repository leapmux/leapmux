import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, assistantBubbles, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

const KIMI = AgentProvider.KIMI_CODE

kimiTest.describe('uses Kimi Code tools', () => {
  // Always Ask stops every command and edit at a banner. These cases assert
  // what a tool DRAWS, so Never Ask takes the banner out of the way; the
  // control-request spec covers the banner itself.
  //
  // Each command prints a number that its own text does not state, such as
  // `kimi-test-output-42` from `kimi-test-output-$((40 + 2))`. The row's header
  // shows the command, so a marker that the command text holds matches the row
  // whether or not the command ran.
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
  })

  kimiTest('a command renders as a tool card with its output, and keeps it after a reload', async ({ page, modelScript }) => {
    await modelScript.queue(
      { toolCalls: [bashToolCall(KIMI, 'echo-call', 'echo "kimi-test-output-$((40 + 2))"')] },
      { text: 'The command printed its output.' },
    )
    await sendMessage(page, modelScript.prompt('Run the echo command and report the output.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const toolMessages = page.locator('[data-tool-message]:visible')
    await expect(toolMessages.filter({ hasText: 'kimi-test-output-42' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The command printed its output.' })).not.toHaveCount(0)

    await page.reload()
    await expect(toolMessages.filter({ hasText: 'kimi-test-output-42' }).first()).toBeVisible()
  })

  // Kimi Code states a failed command's exit code only as a trailer on the
  // output, and the plugin reads it from there.
  kimiTest('a failed command shows its output and its exit code', async ({ page, modelScript }) => {
    await modelScript.queue(
      { toolCalls: [bashToolCall(KIMI, 'exit-call', `sh -c 'echo "hello-from-kimi-$((40 + 2))"; exit 7'`)] },
      { text: 'The command exited with status 7.' },
    )
    await sendMessage(page, modelScript.prompt('Run this exact command and report its result.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const toolMessages = page.locator('[data-tool-message]:visible')
    await expect(toolMessages.filter({ hasText: 'hello-from-kimi-42' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'Error (exit 7)' }).first()).toBeVisible()
    // The trailer is a fact the card states, not output it repeats.
    await expect(toolMessages.filter({ hasText: 'Command failed with exit code' })).toHaveCount(0)
  })
})

kimiTest('preserves a literal private shell path with spaces and metacharacters', async ({ authenticatedKimiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  await exerciseShellToolExecution(context, { includeFailure: false, prepare: () => applyPermissionPreset(page, 'bypass') })
})
