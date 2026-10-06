import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'

import { clineTest } from '../cline-fixtures'
import { contentText } from '../helpers/mockModelScript'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chatText, toolRows } from '../helpers/ui'

/**
 * The installed agent executes the scripted shell command. Calculated output proves that the executor ran the command.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
/**
 * The text of the tool message that answers one call in a Chat Completions request.
 * Cline calls the mock through its DeepSeek provider, which speaks that
 * protocol.
 */
function toolMessageText(body: unknown, callId: string): string {
  const messages = isObject(body) && Array.isArray(body.messages) ? body.messages : []
  return messages
    .filter((message): message is Record<string, unknown> => isObject(message) && message.role === 'tool' && message.tool_call_id === callId)
    .map(message => contentText(message.content))
    .join('\n')
}

clineTest.describe('Cline tool execution', () => {
  clineTest('draws the output of a command', async ({ native }) => {
    // The command text states no `cline-42`, so only the command's own output can put
    // it on the page.
    const { resultRequest } = await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'echo-call', 'echo "cline-$((40 + 2))"')],
      prompt: 'Run the arithmetic command.',
      answer: 'The command printed its number.',
    })

    await expect.poll(() => chatText(native.page)).toContain('cline-42')
    // Cline states the result as a list of records. The row draws the output, not the
    // record.
    expect(await chatText(native.page)).not.toContain('"success"')
    // The executor ran the call: its record reached the next model call.
    expect(JSON.stringify(resultRequest.body)).toContain('cline-42')
  })

  clineTest('draws the error of a failed command, and the model reads why', async ({ native }) => {
    // The command text states no `cline-fail-77`, so only the command's own stderr can
    // put it on the page or in the next model call. Both also carry the command text,
    // so a marker that the command text spells proves nothing.
    const { resultRequest } = await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'fail-call', 'echo "cline-fail-$((70 + 7))" >&2; exit 3')],
      prompt: 'Run the failing command.',
      answer: 'The command failed.',
    })

    // A collapsed row shows the stderr text, so the reader sees why with no expansion.
    const tools = toolRows(native.page)
    await expect(tools.filter({ hasText: 'cline-fail-77' }).first()).toBeVisible()
    // Cline states the exit code in words only, and the command header reads it.
    await expect(tools.filter({ hasText: 'Error (exit 3)' }).first()).toBeVisible()
    // The header states the code. The body does not state it again.
    await expect(tools.filter({ hasText: 'Command exited with code' })).toHaveCount(0)
    // The model reads why: the result of this call states the stderr text and the code.
    const answer = toolMessageText(resultRequest.body, 'fail-call')
    expect(answer).toContain('cline-fail-77')
    expect(answer).toContain('Command exited with code 3')
  })
})

clineTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  await exerciseShellToolExecution(native, { includeFailure: false, prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
