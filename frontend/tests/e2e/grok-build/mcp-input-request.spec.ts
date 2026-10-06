import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { GROK_AGENT, grokTest } from '../grok-fixtures'
import { writeMcpNameFormServer } from '../helpers/mcpNameFormServer'
import { mcpServersConfig } from '../helpers/mcpProbeServer'
import { nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { answerControl, assistantBubbles, expectNoControlBanner, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { createGitRepo } from '../helpers/worktree'
import { nativeContext } from './scenarios'

grokTest.describe('Grok Build settings, folder trust and MCP forms', () => {
  // A repository that holds its own MCP server is one Grok asks about before it
  // loads anything from it. Trusting it loads the server, and the server's form
  // then round-trips through Grok's own elicitation request.
  grokTest('trusts a repository, loads its MCP server and answers the server\'s form', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const repository = createGitRepo(createTestDirectory('grok-trust-'), 'repo')
    const name = 'grok-e2e'
    const server = writeMcpNameFormServer(repository, { serverName: 'form_probe', expectedName: name })
    writeFileSync(join(repository, '.mcp.json'), JSON.stringify(mcpServersConfig(server)))
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { workingDir: repository })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })

    const trust = await waitForControlBanner(page)
    await expect(trust).toContainText('Trust the workspace')
    await expect(trust).toContainText('mcp')
    expect(existsSync(server.ready)).toBe(false)
    await answerControl(page, 'allow')
    await expectNoControlBanner(page)
    await expect(messageBubbles(page).filter({ hasText: 'Trust this workspace' }).first()).toBeVisible()
    // Grok loads the trusted server in place; its tool exists once Grok lists it.
    await expect.poll(() => existsSync(server.ready)).toBe(true)

    const callId = 'grok-mcp'
    const answer = 'The form came back.'
    const start = await modelScript.queue(
      { toolCalls: [mcpToolCall(context.provider, callId, { server: server.name, tool: 'ask', input: {} })] },
      nativeTextStep(context, answer),
    )
    await sendMessage(page, modelScript.prompt('Call the probe form tool.'))
    await modelScript.waitForSteps(start + 1)
    // Grok's `ask` mode requests permission before the MCP tool runs.
    // Allow that request first. The server's form follows after the native tool starts.
    const form = page.getByTestId('elicitation-form').filter({ visible: true })
    const banner = await waitForControlBanner(page)
    await expect(form).toHaveCount(0)
    await answerControl(page, 'allow')
    await expect(form).toBeVisible()
    await expect(banner).toContainText('Name the probe.')
    await form.getByLabel('Name *').fill(name)
    await answerControl(page, 'allow')
    await expectNoControlBanner(page)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect(nativeToolResult(await modelScript.requestAt(start + 1), callId)).toContain('FORM_ROUND_TRIP_OK')
    await expect(assistantBubbles(page).filter({ hasText: answer })).toBeVisible()
  })
})
