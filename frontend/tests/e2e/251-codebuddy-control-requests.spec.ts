import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { bashToolCall } from './helpers/providerToolCalls'
import {
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 251 — CodeBuddy Code control requests.
 *
 * The agent opens in Default, which raises a banner for each write call. The
 * spec allows one Bash write and refuses another, then checks both file effects.
 * The wire answer the worker sends is CodeBuddy's own
 * `{"allowed":true}`, not Claude's `{"behavior":"allow"}`.
 */
codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

codebuddyTest.describe('CodeBuddy Code control requests', () => {
  codebuddyTest('raises a banner for a tool call and runs it once allowed', async ({ askingCodebuddyWorkspace, page, modelScript }) => {
    const output = join(askingCodebuddyWorkspace.workingDir, 'codebuddy-permission-allowed.txt')
    const command = 'printf CODEBUDDY_ALLOWED > ./codebuddy-permission-allowed.txt'
    const call = bashToolCall(AgentProvider.CODEBUDDY, 'call-1', command)
    await modelScript.queue({ toolCalls: [call] })
    await modelScript.queue({ text: 'The command ran.' })
    await sendMessage(page, modelScript.prompt('Run the scripted write command.'))

    // Wait for the model to answer with the tool call BEFORE asserting the
    // banner: an agent process takes tens of seconds to start, and the banner
    // assertion's own 30s timeout would otherwise expire before the turn runs.
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText(command)
    expect(existsSync(output)).toBe(false)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner).toHaveCount(0)
    expect(readFileSync(output, 'utf8')).toBe('CODEBUDDY_ALLOWED')
  })

  codebuddyTest('does not run a denied write command', async ({ askingCodebuddyWorkspace, page, modelScript }) => {
    const output = join(askingCodebuddyWorkspace.workingDir, 'codebuddy-permission-denied.txt')
    const command = 'printf CODEBUDDY_DENIED > ./codebuddy-permission-denied.txt'
    await modelScript.queue({ toolCalls: [bashToolCall(AgentProvider.CODEBUDDY, 'denied-call', command)] })
    await modelScript.fallback({ text: 'The denied call ended.' })
    await sendMessage(page, modelScript.prompt('Try the scripted write command.'))
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText(command)
    expect(existsSync(output)).toBe(false)
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await waitForAgentIdle(page, 180_000)
    await expect(banner).toHaveCount(0)
    expect(existsSync(output)).toBe(false)
  })
})
