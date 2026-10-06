import { expect } from '@playwright/test'
import { clineTest } from '../cline-fixtures'
import { contentText } from '../helpers/mockModelScript'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResultContent } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chatText, toolRows } from '../helpers/ui'

/**
 * The installed agent executes the scripted shell command. Calculated output proves that the executor ran the command.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 */
clineTest.describe('Cline tool execution', () => {
  clineTest('draws the output of a command', async ({ native }) => {
    // The shared scenario proves the computed output on the page and in the next model request.
    await exerciseShellToolExecution(native, {
      includeFailure: false,
      // Cline states the result as a list of records. The row draws the output, not the record.
      rowProof: async ({ page }) => {
        expect(await chatText(page)).not.toContain('"success"')
      },
    })
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
    const answer = contentText(nativeToolResultContent(resultRequest, 'fail-call'))
    expect(answer).toContain('cline-fail-77')
    expect(answer).toContain('Command exited with code 3')
  })
})

clineTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  await exerciseShellToolExecution(native, { includeFailure: false, prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
