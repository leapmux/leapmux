import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { finishCleanup } from '../helpers/cleanup'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { backgroundBashToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, requireRegistryRow } from '../helpers/subagentRegistry'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { applyPermissionPreset, assistantBubbles, expectSettingsChip, sendMessage, waitForSettingsHydrated } from '../helpers/ui'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest.describe('runs Kimi Code subagents and background tasks', () => {
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
  })

  kimiTest('a background command opens a shell row that ends, and its notification turn runs', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
    await expectNoRegistryRows(page, leapmuxServer)
    const control = createToolOutputControl((await currentNativeAgent(context)).workingDir)
    const rule = 'the notification turn after the background command'
    await modelScript.rule({ name: rule, when: { user: '<notification' }, respond: { text: 'The background command finished.' } })
    try {
      await modelScript.queue(
        { toolCalls: [backgroundBashToolCall(AgentProvider.KIMI_CODE, 'bg-shell', control.command)] },
        { text: 'I started the command in the background.' },
      )
      await sendMessage(page, modelScript.prompt('Run the command in the background.'))
      await modelScript.waitForSteps()
      await control.waitForFirstOutput()
      const row = await requireRegistryRow(page, 'shell')
      await expect(row).toHaveAttribute('data-status', 'running')
      await control.releaseFirstOutput()
      await control.waitForSecondOutput()
      await control.releaseFinalOutput()
      await expectRowBecomesFinal(page, row)
      await expect.poll(async () => (await modelScript.status()).ruleMatches[rule] ?? 0).toBeGreaterThan(0)
      await expect(assistantBubbles(page).filter({ hasText: 'The background command finished.' })).not.toHaveCount(0)
      const notification = (await modelScript.status()).requests.find(request => request.rule === rule)
      expect(JSON.stringify(notification?.body)).toContain(control.secondMarker)
    }
    finally {
      await finishCleanup([control.releaseFirstOutput(), control.releaseFinalOutput()])
    }
  })
})
