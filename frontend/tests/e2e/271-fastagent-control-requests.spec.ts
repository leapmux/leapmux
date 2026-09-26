import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest, openFastAgentAgent } from './fastagent-fixtures'
import { bashToolCall, writeToolCall } from './helpers/providerToolCalls'
import { openWorkspace, sendMessage, waitForAgentIdle } from './helpers/ui'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.FAST_AGENT

/**
 * 271 — Fast Agent control requests.
 *
 * Fast Agent gates only MCP tools and shell commands (matrix note 26); its
 * local filesystem tools never ask. LeapMux launches it with
 * `--no-permissions`, which installs no permission handler at all, so a gated
 * call is auto-allowed and no banner can appear. These tests pin that policy:
 * both askable shapes run with no banner, and only a real run prints each
 * marker.
 */
fastAgentTest.describe('Fast Agent control requests', () => {
  fastAgentTest('runs a shell command with no banner under the launch auto-allow', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'fa-shell', 'echo "fa-shell-$((40 + 2))"')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'fa-shell-42' }).first()).toBeVisible()
  })

  fastAgentTest('runs a local write with no banner', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const written = join(workingDir, 'fa-local.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue(
      { toolCalls: [writeToolCall(PROVIDER, 'fa-write', { path: written, content: 'fa-local-content' })] },
      { text: 'The file is written.' },
    )
    await sendMessage(page, modelScript.prompt('Write the scripted file.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    expect(existsSync(written)).toBe(true)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'fa-local.txt' }).first()).toBeVisible()
  })
})
