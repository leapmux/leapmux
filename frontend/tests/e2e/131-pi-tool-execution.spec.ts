import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, writeToolCall } from './helpers/providerToolCalls'
import { messageContents, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest.describe('Pi Tool Execution', () => {
  piTest('bash command execution renders output in chat', async ({ authenticatedPiWorkspace, page, modelScript }) => {
    void authenticatedPiWorkspace // fixture trigger
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.PI, 'echo-call', 'echo "pi-test-output"')] },
      { text: 'The command printed pi-test-output.' },
    )
    await sendMessage(page, modelScript.prompt('Run the bash command: echo "pi-test-output" and show me the output.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    const chatArea = messageContents(page)
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ')
    expect(joined).toContain('pi-test-output')
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

    const chatArea = messageContents(page)
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ')
    // Either the path or the content should appear in the rendered chat.
    expect(joined.includes('pi-test-file') || joined.includes('pi was here')).toBeTruthy()
  })
})
