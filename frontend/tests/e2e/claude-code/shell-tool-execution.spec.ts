import { readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, chooseSettingsOption, expectAssistantAnswer, expectSettingsChip, sendMessage, waitForSettingsIdle } from '../helpers/ui'

/**
 * The heartbeat case checks a native CLI tool_progress event in the browser.
 * Backend and component tests cover the handler, storage, transport, and badge separately.
 * This case checks their complete path and badge removal after the actual tool ends.
 *
 * Claude emits the first heartbeat after thirty seconds. The command must remain active past that interval.
 * Default permission mode can stop Bash before it starts. Apply Bypass before the command.
 * The command starts a private HTTP server. An explicit request ends it without a fixed duration.
 * The configured agent refuses file-polling loops, so the command uses this request protocol.
 */
/**
 * The native timer needs an assertion deadline longer than the project's thirty-second default.
 * Installed Claude 2.1.284 uses IEr=30000 in YFn. The timer reads no environment override.
 * Worker delivery and browser rendering can delay the first event beyond that default deadline.
 * Ninety seconds includes another heartbeat and remains below the test deadline.
 */
const FIRST_HEARTBEAT_DEADLINE_MS = 90_000

/** Wait for the native command to report the port that its server actually binds. */
async function waitForBoundPort(portFile: string): Promise<number> {
  let port = 0
  await expect.poll(async () => {
    port = Number.parseInt(await readFile(portFile, 'utf8').catch(() => ''), 10)
    return Number.isInteger(port) && port > 0
  }, { message: `the scripted server never reported a port at ${portFile}` }).toBe(true)
  return port
}

claudeTest.describe('Tool Running Badge', () => {
  claudeTest('shows a long Claude tool\'s elapsed time, and clears it when the tool ends', async ({ page, authenticatedWorkspace, modelScript }) => {
    const dir = createTestDirectory('leapmux-badge-')
    const script = join(dir, 'tool-server.mjs')
    const portFile = join(dir, 'port')
    // Port zero lets the operating system reserve the port when the native server starts.
    // An earlier findFreePort result could become unavailable before native startup finishes.
    await writeFile(script, `
import { createServer } from 'node:http'
import { writeFileSync } from 'node:fs'
const server = createServer((_request, response) => {
  response.end('DONE')
  server.close()
})
server.listen(0, '127.0.0.1', () => {
  writeFileSync(${JSON.stringify(portFile)}, String(server.address().port))
})
setTimeout(() => server.close(), 180000).unref()
`)

    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await expect(page.getByText(/^Starting /)).not.toBeVisible()

    // Default permission mode can stop Bash before it starts. Bypass permits the actual heartbeat command.
    await chooseSettingsOption(page, 'permissionMode-bypassPermissions')
    await expectSettingsChip(page, 'Bypass Permissions')
    await waitForSettingsIdle(page)

    // The setting can restart the native process. Require a real model reply after the change.
    // The chip and spinner alone do not prove that the restarted process accepts input.
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(1)
    await expectAssistantAnswer(page)

    // Script the Bash decision. The actual native command supplies the heartbeat.
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.CLAUDE_CODE, 'run-server', `node ${quotePosixShellArgument(script)}`)] })
    await sendMessage(page, modelScript.prompt(`Run the Node.js integration test server at ${script} in the foreground.`))
    await modelScript.waitForSteps(2)
    // The native tool card proves delivery. An absent failure banner can pass before the send reply arrives.
    // Visible locators exclude hidden premeasure rows.
    await expect(page.locator('[data-tool-message]:visible').first()).toBeVisible()

    // Require the server's actual port file. A tool that fails immediately can still create a card without a heartbeat.
    const port = await waitForBoundPort(portFile)

    const badge = page.locator('[data-testid="tool-running-badge"]:visible')

    // Use the existing native timer exception described by FIRST_HEARTBEAT_DEADLINE_MS.
    await expect(badge).toBeVisible({ timeout: FIRST_HEARTBEAT_DEADLINE_MS })
    // Keep the anchored duration format. A delayed first event can report a later heartbeat.
    // Reject NaNs, decimal seconds, and an empty label.
    await expect(badge).toHaveText(/^\d+[dhms]( \d+[hms])*$/)

    // Queue the continuation before the native tool can complete and request its next model turn.
    await modelScript.queue({ text: 'DONE' })

    // End the real tool and require the browser to remove its badge.
    const response = await fetch(`http://127.0.0.1:${port}/complete`)
    expect(response.ok).toBe(true)
    expect(await response.text()).toBe('DONE')
    await expect(badge).not.toBeVisible()
  })
})

claudeTest('returns native shell success and failure output to the following model request', async ({ authenticatedClaudeWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseShellToolExecution({ page, modelScript, leapmuxServer, workspaceId: authenticatedClaudeWorkspace.workspaceId, provider: AgentProvider.CLAUDE_CODE })
})
