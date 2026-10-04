import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { expectCompactionNotice } from '../helpers/compaction'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle } from '../helpers/ui'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest.describe('Cline compaction notice', () => {
  clineTest('shows the native notice after context overflow recovery', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    const oldMarker = 'OLDER_TOOL_CONTEXT'
    const command = `node -e "process.stdout.write(('OLDER_' + 'TOOL_CONTEXT ').repeat(2000))"`
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CLINE, 'large-tool-result', command)] },
      { text: 'The earlier tool output is recorded.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted command and record its output.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // Native basic compaction preserves the three newest assistant answers.
    // The older tool result supplies content that it can remove on overflow.
    for (let turn = 1; turn <= 3; turn++) {
      await modelScript.queue({ text: `Recent answer ${turn}.` })
      await sendMessage(page, modelScript.prompt(`Record recent step ${turn}.`))
      await modelScript.waitForSteps()
      await waitForAgentIdle(page)
    }

    await modelScript.queue(
      { error: { status: 400, code: 'context_length_exceeded', message: 'The maximum context length was exceeded.' } },
      { text: 'Recovered after compaction.' },
    )
    await sendMessage(page, modelScript.prompt('Reply after the context overflow recovery.'))
    await modelScript.waitForSteps(6)
    const before = (await modelScript.status()).requests.find(request => request.stepIndex === 5)
    expect(before?.protocol).toBe('openai-chat-completions')
    expect(JSON.stringify(before?.body).includes(oldMarker)).toBe(true)

    await modelScript.waitForSteps()
    await expectCompactionNotice(page)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Recovered after compaction.' }).first()).toBeVisible()

    const retry = (await modelScript.status()).requests.find(request => request.stepIndex === 6)
    expect(retry?.protocol).toBe('openai-chat-completions')
    const retryBody = JSON.stringify(retry?.body)
    expect(retryBody.includes(oldMarker)).toBe(false)
    expect(retryBody.includes('Reply after the context overflow recovery.')).toBe(true)
  })
})
