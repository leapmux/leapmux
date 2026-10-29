import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { writeMcpEchoServer } from '../helpers/mcpEchoServer'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeModelToolNames } from '../helpers/nativeScenario'
import { withNativeWorker } from '../helpers/nativeWorker'
import { PROJECT_MCP_SERVER_NAME } from '../helpers/nativeWorkspaceTrustLimit'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { answerControl, assistantBubbles, expectNoControlBanner, openWorkspace, waitForControlBanner } from '../helpers/ui'
import { openProviderAgent, withTestWorkspace } from '../helpers/workspace'
import { ensureGitRepositoryRoot } from '../helpers/worktree'
import { kimiTest } from '../kimi-fixtures'
import { KIMI_AGENT, nativeContext } from './scenarios'

/** The answer of the one turn after the decision. */
const TRUST_TURN_ANSWER = 'The workspace trust decision reached this turn.'

// Kimi Code loads a project's MCP servers, skills and instructions only from a workspace it trusts, and `kimi web`
// asks nothing. The Worker asks instead. Trust grants the workspace, and because a running session keeps what it
// loaded, the Worker replaces the process, whose session then loads the project.
for (const decision of ['allow', 'deny'] as const) {
  kimiTest(`keeps the project's MCP servers unloaded until the workspace trust decision is ${decision}`, async ({ page, modelScript, leapmuxServer, authenticatedEmptyWorkspace }) => {
    // The fixture signs the page in. The question opens in a workspace of the private Worker below.
    void authenticatedEmptyWorkspace
    // The shared Kimi environment trusts every workspace, so this Worker clears that switch.
    await withNativeWorker(leapmuxServer, {
      dataDirPrefix: 'kimi-trust-worker',
      workerName: 'Kimi workspace trust',
      env: { KIMI_CODE_TRUST_WORKSPACE: '0' },
    }, async ({ server }) => {
      await withTestWorkspace(server, 'kimi-trust', async ({ workspaceId }) => {
        // Kimi reads the `.mcp.json` of the nearest git root, so the project is a repository of its own.
        const workingDir = newProviderWorkingDir(KIMI_AGENT, 'kimi-trust-project-')
        ensureGitRepositoryRoot(workingDir)
        const receiptLog = join(workingDir, 'trust-mcp-receipt.json')
        const echo = writeMcpEchoServer(workingDir, { receiptLog })
        writeFileSync(join(workingDir, '.mcp.json'), JSON.stringify({
          mcpServers: {
            [PROJECT_MCP_SERVER_NAME]: {
              transport: 'stdio',
              command: echo.command,
              args: [...echo.args],
            },
          },
        }))
        await openProviderAgent(server, workspaceId, KIMI_AGENT, { workingDir })
        await openWorkspace(page, workspaceId)
        const banner = await waitForControlBanner(page)
        await expect(banner).toContainText(`Trust the folder ${workingDir}`)
        await expect(banner).toContainText('Kimi Code loads the MCP servers, skills and instructions of a project only from a folder that you trust')
        const context = await nativeContext({
          page,
          modelScript,
          leapmuxServer: server,
          workspaceId,
        })
        const before = await currentNativeAgent(context)

        await answerControl(page, decision)
        await expectNoControlBanner(page)
        const request = await sendNativeAnswer(context, 'Reply once after the workspace trust decision.', TRUST_TURN_ANSWER)
        const projectTools = nativeModelToolNames(request).filter(name => name.includes(PROJECT_MCP_SERVER_NAME))
        if (decision === 'allow') {
          expect(projectTools, 'the model sees the tools of the project server').not.toEqual([])
          expect(existsSync(receiptLog), 'the project server started').toBe(true)
        }
        else {
          expect(projectTools, 'the model sees no tool of the project server').toEqual([])
          expect(existsSync(receiptLog), 'the project server never started').toBe(false)
        }
        // The decision keeps the agent and its folder. A Trust replaces the process, and the Worker resumes the stored
        // session only when it holds a conversation: the question comes before the first turn, so none is lost.
        const after = await currentNativeAgent(context)
        expect(after.id).toBe(before.id)
        expect(after.workingDir).toBe(workingDir)

        // The answered question stays answered.
        await page.reload()
        await expect(assistantBubbles(page).filter({ hasText: TRUST_TURN_ANSWER }).first()).toBeVisible()
        await expectNoControlBanner(page)
      })
    })
  })
}

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/522.
