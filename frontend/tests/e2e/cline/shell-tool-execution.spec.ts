import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { contentText, isRecord } from '../helpers/mockModelScript'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chatText, sendMessage, waitForAgentIdle } from '../helpers/ui'

/**
 * The installed agent executes the scripted shell command. Calculated output proves that the executor ran the command.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

/**
 * The text of the tool message that answers one call in a Chat Completions request.
 * Cline calls the mock through its DeepSeek provider, which speaks that
 * protocol.
 */
function toolMessageText(body: unknown, callId: string): string {
  const messages = isRecord(body) && Array.isArray(body.messages) ? body.messages : []
  return messages
    .filter((message): message is Record<string, unknown> => isRecord(message) && message.role === 'tool' && message.tool_call_id === callId)
    .map(message => contentText(message.content))
    .join('\n')
}

clineTest.describe('Cline tool execution', () => {
  clineTest('draws the output of a command', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    // The command text states no `cline-42`, so only the command's own output can put
    // it on the page.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CLINE, 'echo-call', 'echo "cline-$((40 + 2))"')] },
      { text: 'The command printed its number.' },
    )
    await sendMessage(page, modelScript.prompt('Run the arithmetic command.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expect.poll(() => chatText(page)).toContain('cline-42')
    // Cline states the result as a list of records. The row draws the output, not the
    // record.
    expect(await chatText(page)).not.toContain('"success"')
    // The executor ran the call: its record reached the next model call.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    expect(JSON.stringify(followUp?.body)).toContain('cline-42')
  })

  clineTest('draws the error of a failed command, and the model reads why', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    // The command text states no `cline-fail-77`, so only the command's own stderr can
    // put it on the page or in the next model call. Both also carry the command text,
    // so a marker that the command text spells proves nothing.
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.CLINE, 'fail-call', 'echo "cline-fail-$((70 + 7))" >&2; exit 3')] },
      { text: 'The command failed.' },
    )
    await sendMessage(page, modelScript.prompt('Run the failing command.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    // A collapsed row shows the stderr text, so the reader sees why with no expansion.
    const tools = page.locator('[data-tool-message]:visible')
    await expect(tools.filter({ hasText: 'cline-fail-77' }).first()).toBeVisible()
    // Cline states the exit code in words only, and the command header reads it.
    await expect(tools.filter({ hasText: 'Error (exit 3)' }).first()).toBeVisible()
    // The header states the code. The body does not state it again.
    await expect(tools.filter({ hasText: 'Command exited with code' })).toHaveCount(0)
    // The model reads why: the result of this call states the stderr text and the code.
    const followUp = status.requests.find(request => request.stepIndex === 1)
    const answer = toolMessageText(followUp?.body, 'fail-call')
    expect(answer).toContain('cline-fail-77')
    expect(answer).toContain('Command exited with code 3')
  })
})

clineTest('preserves a literal private shell path with spaces and metacharacters', async ({ authenticatedClineWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  await exerciseShellToolExecution(context, { includeFailure: false, prepare: () => applyPermissionPreset(page, 'bypass') })
})
