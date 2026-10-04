import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest, openFastAgentAgent } from '../fastagent-fixtures'
import { writeMcpPermissionServer } from '../helpers/mcpPermissionServer'
import { bashToolCall, mcpToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { messageContents, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

fastAgentTest.describe('Fast Agent control requests', () => {
  fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

  const PROVIDER = AgentProvider.FAST_AGENT

  fastAgentTest('runs a shell command after the reader allows it', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'fa-shell', 'echo "fa-shell-$((40 + 2))"')] },
      { text: 'The command ran.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command.'))
    await modelScript.waitForSteps(1)

    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toContainText('execute')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(banner).toHaveCount(0)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'fa-shell-42' }).first()).toBeVisible()
  })

  fastAgentTest('denies a local write and leaves no file', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const written = join(workingDir, 'fa-local.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue(
      { toolCalls: [writeToolCall(PROVIDER, 'fa-write', { path: written, content: 'fa-local-content' })] },
      { text: 'The file was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Write the scripted file.'))
    await modelScript.waitForSteps(1)

    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toContainText('write_text_file')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(banner).toHaveCount(0)
    expect(existsSync(written)).toBe(false)
    await expect(messageContents(page).filter({ hasText: 'The user has declined permission to use this tool' }).first()).toBeVisible()
  })

  fastAgentTest('denies a shell command before it writes a file', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const written = join(workingDir, 'fa-shell-denied.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'fa-shell-deny', `printf 'forbidden' > ${JSON.stringify(written)}`)] },
      { text: 'The shell command was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted shell command.'))
    await modelScript.waitForSteps(1)

    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toContainText('execute')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(banner).toHaveCount(0)
    expect(existsSync(written)).toBe(false)
  })

  fastAgentTest('writes a local file after the reader allows it', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const written = join(workingDir, 'fa-local-allowed.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    await modelScript.queue(
      { toolCalls: [writeToolCall(PROVIDER, 'fa-write-allow', { path: written, content: 'FA_FILE_ALLOWED_MARKER' })] },
      { text: 'The file was written.' },
    )
    await sendMessage(page, modelScript.prompt('Write the scripted file.'))
    await modelScript.waitForSteps(1)

    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toContainText('write_text_file')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(banner).toHaveCount(0)
    expect(readFileSync(written, 'utf8')).toBe('FA_FILE_ALLOWED_MARKER')
  })

  fastAgentTest('denies an MCP tool before the server receives it', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const server = writeMcpPermissionServer(workingDir)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await sendMessage(page, `/mcp connect --name permission_probe ${JSON.stringify(server.command)} ${JSON.stringify(server.script)}`)
    await waitForAgentIdle(page, 120_000)
    await expect.poll(() => existsSync(server.ready)).toBe(true)

    await modelScript.queue(
      { toolCalls: [mcpToolCall(PROVIDER, 'fa-mcp-deny', { server: 'permission_probe', tool: 'touch', input: {} })] },
      { text: 'The MCP tool was refused.' },
    )
    await sendMessage(page, modelScript.prompt('Call permission_probe touch.'))
    await modelScript.waitForSteps(1)

    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect(banner).toContainText('touch')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expect(banner).toHaveCount(0)
    expect(existsSync(server.called)).toBe(false)
  })
})
