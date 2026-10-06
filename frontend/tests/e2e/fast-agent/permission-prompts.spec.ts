import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, fastAgentTest, openFastAgentAgent } from '../fastagent-fixtures'
import { writeMcpPermissionServer } from '../helpers/mcpPermissionServer'
import { expectDeclinedToolRow } from '../helpers/nativePermission'
import { bashToolCall, mcpToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { messageBubbles, messageContents, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'

fastAgentTest.describe('Fast Agent control requests', () => {
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
    await waitForAgentIdle(page)

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
    await waitForAgentIdle(page)

    await expect(banner).toHaveCount(0)
    expect(existsSync(written)).toBe(false)
    const refusal = 'The user has declined permission to use this tool'
    await expect(messageContents(page).filter({ hasText: refusal }).first()).toBeVisible()
    // Fast Agent gives the call an identifier of its own, so the result row is found by
    // its refusal.
    const refused = messageBubbles(page).and(page.locator('[data-tool-row-role="result"]')).filter({ hasText: refusal }).first()
    const callId = await refused.getAttribute('data-tool-call-id')
    if (!callId)
      throw new Error('The refused Fast Agent write drew no tool row.')
    // The refused write reads declined, and its result row states the refusal. The request
    // row heads the call with the file, because a paired result row draws no header. Fast
    // Agent streams the call, so the opening frame states no path. The path reaches the
    // client in a content diff, which the refusal replaces, and in the permission request.
    // The Worker stores the input of the permission request with the request row. The
    // checks hold before and after a reload.
    const request = messageBubbles(page).and(page.locator('[data-tool-row-role="request"]')).and(page.locator(`[data-tool-call-id="${callId}"]`))
    for (const reload of [false, true]) {
      if (reload) {
        await page.reload()
        await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
      }
      await expectDeclinedToolRow(page, callId, refusal)
      await expect(request).toHaveAttribute('data-tool-status', 'declined')
      await expect(request).toContainText('fa-local.txt')
    }
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
    await waitForAgentIdle(page)

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
    await waitForAgentIdle(page)

    await expect(banner).toHaveCount(0)
    expect(readFileSync(written, 'utf8')).toBe('FA_FILE_ALLOWED_MARKER')
  })

  fastAgentTest('denies an MCP tool before the server receives it', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    const server = writeMcpPermissionServer(workingDir)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await sendMessage(page, `/mcp connect --name permission_probe ${JSON.stringify(server.command)} ${JSON.stringify(server.script)}`)
    await waitForAgentIdle(page)
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
    await waitForAgentIdle(page)

    await expect(banner).toHaveCount(0)
    expect(existsSync(server.called)).toBe(false)
  })
})
