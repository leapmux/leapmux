import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, fastAgentTest } from '../fastagent-fixtures'
import { writeMcpPermissionServer } from '../helpers/mcpPermissionServer'
import { exerciseNativePermissionDecision, expectDeclinedToolRow } from '../helpers/nativePermission'
import { bashToolCall, mcpToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { expectNoControlBanner, messageBubbles, messageContents, openWorkspace, toolCallRow, toolRows } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { connectNativeMcp } from './mcpScenarios'
import { FAST_AGENT_AGENT, nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent control requests', () => {
  fastAgentTest('runs a shell command after the reader allows it', async ({ native }) => {
    await exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, 'fa-shell', 'echo "fa-shell-$((40 + 2))"'),
      decision: 'allow',
      beforeDecision: banner => expect(banner).toContainText('execute'),
      // The follow-up request carries the output of the command that ran.
      nativeProof: request => expect(JSON.stringify(request.body)).toContain('fa-shell-42'),
      viewProof: async () => {
        await expectNoControlBanner(native.page)
        await expect(toolRows(native.page).filter({ hasText: 'fa-shell-42' }).first()).toBeVisible()
      },
    })
  })

  fastAgentTest('denies a local write and leaves no file', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    const written = join(workingDir, 'fa-local.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    const refusal = 'The user has declined permission to use this tool'
    await exerciseNativePermissionDecision(context, {
      toolCall: writeToolCall(context.provider, 'fa-write', { path: written, content: 'fa-local-content' }),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText('write_text_file'),
      nativeProof: () => {
        expect(existsSync(written)).toBe(false)
      },
      viewProof: async () => {
        await expectNoControlBanner(page)
        await expect(messageContents(page).filter({ hasText: refusal }).first()).toBeVisible()
        // Fast Agent gives the call an identifier of its own, so the test finds the result row by its refusal.
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
        const request = toolCallRow(page, callId, 'request')
        for (const reload of [false, true]) {
          if (reload) {
            await page.reload()
            await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
          }
          await expectDeclinedToolRow(page, callId, refusal)
          await expect(request).toHaveAttribute('data-tool-status', 'declined')
          await expect(request).toContainText('fa-local.txt')
        }
      },
    })
  })

  fastAgentTest('denies a shell command before it writes a file', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    const written = join(workingDir, 'fa-shell-denied.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(context.provider, 'fa-shell-deny', `printf 'forbidden' > ${JSON.stringify(written)}`),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText('execute'),
      nativeProof: () => {
        expect(existsSync(written)).toBe(false)
      },
      viewProof: () => expectNoControlBanner(page),
    })
  })

  fastAgentTest('writes a local file after the reader allows it', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    const written = join(workingDir, 'fa-local-allowed.txt')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseNativePermissionDecision(context, {
      toolCall: writeToolCall(context.provider, 'fa-write-allow', { path: written, content: 'FA_FILE_ALLOWED_MARKER' }),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('write_text_file')
        expect(existsSync(written)).toBe(false)
      },
      nativeProof: () => {
        expect(readFileSync(written, 'utf8')).toBe('FA_FILE_ALLOWED_MARKER')
      },
      viewProof: () => expectNoControlBanner(page),
    })
  })

  fastAgentTest('denies an MCP tool before the server receives it', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    const server = writeMcpPermissionServer(workingDir)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    // The server writes its ready file once the client lists its tools.
    await connectNativeMcp(context, server, server.ready)
    await exerciseNativePermissionDecision(context, {
      toolCall: mcpToolCall(context.provider, 'fa-mcp-deny', { server: server.name, tool: 'touch', input: {} }),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText('touch'),
      nativeProof: () => {
        expect(existsSync(server.called)).toBe(false)
      },
      viewProof: () => expectNoControlBanner(page),
    })
  })
})
