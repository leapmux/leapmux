import { readFileSync, statSync } from 'node:fs'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { finishCleanup } from '../helpers/cleanup'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { backgroundBashToolCall } from '../helpers/providerToolCalls'
import { getGlobalState } from '../helpers/server'
import { expectNoRegistryRows, expectRowBecomesFinal, requireRegistryRow } from '../helpers/subagentRegistry'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { applyPermissionPreset, assistantBubbles, expectSettingsChip, sendMessage, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('runs Kimi Code subagents and background tasks', () => {
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
  })

  kimiTest('a background command opens a shell row that ends, and its notification turn runs', async ({ native }) => {
    const { page, modelScript, leapmuxServer } = native
    await expectNoRegistryRows(page, leapmuxServer)
    const control = createToolOutputControl((await currentNativeAgent(native)).workingDir)
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
      // Kimi Code does not inline a finished task's output in the notification.
      // It links the saved output file, so the marker proof reads that file.
      const body = notification?.body as { messages?: readonly { content?: unknown }[] } | undefined
      const notificationText = (body?.messages ?? []).map(message => typeof message.content === 'string' ? message.content : '').join('\n')
      const outputFile = /<output-file path="(?<path>[^"<]+)" bytes="(?<bytes>\d+)">/.exec(notificationText)
      if (!outputFile?.groups?.path || !outputFile.groups.bytes)
        throw new Error('The native completion notification carries no output-file pointer.')
      assertPrivateNativePath(outputFile.groups.path, getGlobalState().tmpDir)
      expect(statSync(outputFile.groups.path).size).toBe(Number(outputFile.groups.bytes))
      const output = readFileSync(outputFile.groups.path, 'utf8')
      expect(output).toContain(control.firstMarker)
      expect(output).toContain(control.secondMarker)
    }
    finally {
      await finishCleanup([control.releaseFirstOutput(), control.releaseFinalOutput()])
    }
  })
})
