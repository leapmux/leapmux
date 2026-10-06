import { existsSync } from 'node:fs'
import { expect } from '@playwright/test'
import { writeMcpNameFormServer } from '../helpers/mcpNameFormServer'
import { nativeTextStep, nativeToolOutcome } from '../helpers/nativeScenario'
import { mcpToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { answerControl, assistantBubbles, expectNoControlBanner, openWorkspace, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { KIRO_AGENT, kiroTest } from '../kiro-fixtures'
import { writeKiroProjectMcpServers } from './mcpConfiguration'
import { nativeContext } from './scenarios'

kiroTest.describe('Kiro control requests', () => {
  // A workspace MCP server's form round-trips through Kiro's own elicitation request.
  kiroTest('answers the form that an MCP server asks for', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const name = 'kiro-e2e'
    const server = writeMcpNameFormServer(createTestDirectory('kiro-mcp-form-'), { serverName: 'probe', expectedName: name })
    // The Allow all policy runs the MCP tool without a permission request, so the form is the only request that the
    // call raises.
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, {
      optionValues: { policyPreset: 'allow-all' },
      prepare: (workingDir) => {
        writeKiroProjectMcpServers(workingDir, server)
      },
    })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    // Kiro loads the server in the background. Its tool exists once Kiro lists it.
    await expect.poll(() => existsSync(server.ready)).toBe(true)

    const callId = 'kiro-mcp'
    const answer = 'The form came back.'
    const start = await modelScript.queue(
      { toolCalls: [mcpToolCall(context.provider, callId, { server: server.name, tool: 'ask', input: {} })] },
      nativeTextStep(context, answer),
    )
    await sendMessage(page, modelScript.prompt('Call the probe form tool.'))
    await modelScript.waitForSteps(start + 1)
    const banner = await waitForControlBanner(page)
    const form = page.getByTestId('elicitation-form').filter({ visible: true })
    await expect(form).toBeVisible()
    await expect(banner).toContainText('Name the probe.')
    await form.getByLabel('Name *').fill(name)
    await answerControl(page, 'allow')
    await expectNoControlBanner(page)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect((await nativeToolOutcome(context, await modelScript.requestAt(start + 1), callId)).text).toContain('FORM_ROUND_TRIP_OK')
    await expect(assistantBubbles(page).filter({ hasText: answer })).toBeVisible()
  })
})
