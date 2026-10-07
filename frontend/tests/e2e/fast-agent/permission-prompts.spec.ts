import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { fastAgentTest } from '../fastagent-fixtures'
import { writeMcpPermissionServer } from '../helpers/mcpPermissionServer'
import { exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseRememberedAllow, expectDeclinedToolRow, toolResultCallId } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall, mcpToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { expectNoControlBanner, messageContents, openWorkspace, savedControlAnswer, toolCallRow, toolRows } from '../helpers/ui'
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
        // The saved row reads the name of Fast Agent's own option.
        await expect(savedControlAnswer(native.page)).toHaveText('Allow Once')
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
        await expect(savedControlAnswer(page)).toHaveText('Reject Once')
        // Fast Agent gives the call an identifier of its own, so the test finds the result row by its refusal.
        const callId = await toolResultCallId(page, refusal)
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

  // The ACP reply selects an option, and an option carries no text. The reason follows as the reader's next message.
  fastAgentTest('sends the reader\'s typed refusal reason as the next message', async ({ native }) => {
    const written = join((await currentNativeAgent(native)).workingDir, 'fa-shell-reason.txt')
    await exerciseNativePermissionReason(native, {
      toolCall: bashToolCall(native.provider, 'fa-shell-reason', `printf 'forbidden' > ${JSON.stringify(written)}`),
      route: 'next-message',
      beforeDecision: banner => expect(banner).toContainText('execute'),
      expectNotRun: () => expect(existsSync(written)).toBe(false),
      viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Reject Once'),
    })
  })

  // Fast Agent keeps an always answer for the tool, so a later call of the same tool runs with no request.
  fastAgentTest('an always answer covers the same tool in the next turn', async ({ native }) => {
    const written = join((await currentNativeAgent(native)).workingDir, 'fa-shell-always.txt')
    // Each run appends the marker, so the file states how many runs happened.
    const command = `printf fa-always >> ${JSON.stringify(written)}`
    await exerciseRememberedAllow(native, {
      scope: 'Always',
      firstCall: bashToolCall(native.provider, 'fa-always-first', command),
      secondCall: bashToolCall(native.provider, 'fa-always-second', command),
      beforeDecision: () => expect(existsSync(written)).toBe(false),
      firstProof: () => expect(readFileSync(written, 'utf8')).toBe('fa-always'),
      secondProof: () => expect(readFileSync(written, 'utf8')).toBe('fa-alwaysfa-always'),
      viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Always Allow This Tool'),
    })
  })
})
