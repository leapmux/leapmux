import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { waitForMcpToolListed } from '../helpers/mcpServerReceipt'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { withNativeWorker } from '../helpers/nativeWorker'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { createTestDirectory } from '../helpers/runDirectory'
import { answerControl, assistantBubbles, expectNoControlBanner, openWorkspace, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent, withTestWorkspace } from '../helpers/workspace'
import { COPILOT_AGENT, nativeContext } from './scenarios'

/** The answer of the one turn after the decision. */
const TRUST_TURN_ANSWER = 'The folder trust decision reached this turn.'

/** The folders that a Copilot home trusts. Copilot writes comment lines above the JSON object of its configuration. */
function trustedFolders(copilotHome: string): unknown[] {
  const path = join(copilotHome, 'config.json')
  if (!existsSync(path))
    return []
  const json = readFileSync(path, 'utf8').split('\n').filter(line => !line.trimStart().startsWith('//')).join('\n')
  const folders: unknown = JSON.parse(json).trustedFolders
  return Array.isArray(folders)
    ? folders
    : []
}

// Copilot loads a project's MCP servers only from a folder in its trusted list, and in server mode it asks nothing.
// The Worker asks instead, and Trust adds the folder to Copilot's list and loads the servers in the running session.
for (const decision of ['allow', 'deny'] as const) {
  copilotTest(`keeps the project's MCP servers unloaded until the folder trust decision is ${decision}`, async ({ page, modelScript, leapmuxServer, authenticatedEmptyWorkspace }) => {
    // The fixture signs the page in. The question opens in a workspace of the private Worker below.
    void authenticatedEmptyWorkspace
    // The shared Copilot home trusts the whole run, so this Worker gives Copilot a home that trusts no folder.
    const copilotHome = createTestDirectory('copilot-trust-home-')
    await withNativeWorker(leapmuxServer, {
      dataDirPrefix: 'copilot-trust-worker',
      workerName: 'Copilot folder trust',
      env: { COPILOT_HOME: copilotHome },
      privateDirectories: [copilotHome],
    }, async ({ server }) => {
      await withTestWorkspace(server, 'copilot-trust', async ({ workspaceId }) => {
        const workingDir = newProviderWorkingDir(COPILOT_AGENT, 'copilot-trust-project-')
        const receiptLog = join(workingDir, 'trust-mcp-receipt.json')
        const echo = writeMcpEchoServer(workingDir, { receiptLog })
        writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify({
          mcpServers: {
            [echo.name]: {
              type: 'local',
              command: echo.command,
              args: echo.args,
              tools: ['*'],
            },
          },
        }))
        await openProviderAgent(server, workspaceId, COPILOT_AGENT, { workingDir })
        await openWorkspace(page, workspaceId)
        const banner = await waitForControlBanner(page)
        await expect(banner).toContainText(`Trust the folder ${workingDir}`)
        await expect(banner).toContainText('GitHub Copilot loads the MCP servers of a project only from a folder that you trust')
        expect(existsSync(receiptLog), 'the project server waits for the decision').toBe(false)

        await answerControl(page, decision)
        await expectNoControlBanner(page)
        if (decision === 'allow')
          await waitForMcpToolListed(receiptLog, 'echo')
        const context = await nativeContext({
          page,
          modelScript,
          leapmuxServer: server,
          workspaceId,
        })
        const request = await sendNativeAnswer(context, 'Reply once after the folder trust decision.', TRUST_TURN_ANSWER)
        const projectTools = nativeModelToolNames(request).filter(name => name.includes(echo.name))
        if (decision === 'allow') {
          expect(projectTools, 'the model sees the tools of the project server').not.toEqual([])
          expect(trustedFolders(copilotHome), 'Copilot keeps the folder in its trusted list').toContain(workingDir)
        }
        else {
          expect(projectTools, 'the model sees no tool of the project server').toEqual([])
          expect(existsSync(receiptLog), 'the project server never started').toBe(false)
          expect(trustedFolders(copilotHome), 'the folder stays untrusted').not.toContain(workingDir)
        }

        // The answered question stays answered.
        await page.reload()
        await expect(assistantBubbles(page).filter({ hasText: TRUST_TURN_ANSWER }).first()).toBeVisible()
        await expectNoControlBanner(page)
      })
    })
  })
}

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/522.
