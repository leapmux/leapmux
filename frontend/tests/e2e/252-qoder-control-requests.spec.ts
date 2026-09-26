import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from './helpers/providerToolCalls'
import {
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from './qoder-fixtures'

/**
 * 252 — Qoder CLI control requests.
 *
 * Qoder carries a first-class control_request channel (`--permission-prompt-tool
 * stdio`). The agent opens in Default, which raises a banner for each ASKABLE
 * tool call. Qoder's policy auto-allows read-only shell commands, so the spec
 * scripts a WRITE command -- the kind its policy asks about. The spec answers
 * the banner with Allow and proves the call reaches the transcript.
 */
qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

qoderTest.describe('Qoder CLI control requests', () => {
  qoderTest('raises a banner for a tool call and runs it once allowed', async ({ askingQoderWorkspace, page, modelScript }) => {
    void askingQoderWorkspace
    const command = 'printf hi > ./qoder-control-probe.txt'
    const call = bashToolCall(AgentProvider.QODER, 'call-1', command)
    await modelScript.queue({ toolCalls: [call] })
    await modelScript.queue({ text: 'The command ran.' })
    await sendMessage(page, modelScript.prompt(`Run ${command}.`))

    // Wait for the model to answer with the tool call BEFORE asserting the
    // banner: an agent process takes tens of seconds to start, and the banner
    // assertion's own 30s timeout would otherwise expire before the turn runs.
    await modelScript.waitForSteps(1)
    const banner = page.getByTestId('control-banner').filter({ visible: true })
    await expect(banner).toContainText('qoder-control-probe.txt')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expect(banner).toHaveCount(0)
  })
})
