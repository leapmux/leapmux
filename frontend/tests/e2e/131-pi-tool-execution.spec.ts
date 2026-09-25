import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, writeToolCall } from './helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest.describe('Pi Tool Execution', () => {
  piTest('bash command execution renders output in chat', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    void authenticatedPiWorkspace // fixture trigger
    // The command text, the prompt and the reply state no `pi-42`, so only the
    // command's own output can put it in a tool row.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.PI, 'echo-call', 'echo "pi-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command and show me the output.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'pi-42' }).first()).toBeVisible()
  })

  piTest('write tool creates a file and the chat surfaces the path', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    void authenticatedPiWorkspace // fixture trigger
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.PI, 'write-call', { path: '/tmp/pi-test-file.txt', content: 'pi was here' })] },
      { text: 'I wrote /tmp/pi-test-file.txt.' },
    )
    await sendMessage(page, modelScript.prompt('Use the write tool to create /tmp/pi-test-file.txt with the content "pi was here".'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    // The prompt states the path and the content, so a check of the whole chat
    // passes with no write at all. Only the write call's own row states the path
    // inside a tool row.
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'pi-test-file.txt' }).first()).toBeVisible()
  })
})
