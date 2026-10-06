import { expect } from '@playwright/test'
import { ampTest } from '../amp-fixtures'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chatText } from '../helpers/ui'

/**
 * The installed agent executes the scripted shell command. Calculated output proves that the executor ran the command.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp tool execution', () => {
  ampTest('draws the output of a command', async ({ native }) => {
    // The command text states no `amp-42`, so only the command's own output can put
    // it on the page. `exerciseShellToolExecution` below proves that the result reaches the model.
    await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'echo-call', 'echo "amp-$((40 + 2))"')],
      prompt: 'Run the arithmetic command.',
      answer: 'The command printed its number.',
    })

    await expect.poll(() => chatText(native.page)).toContain('amp-42')
    // Amp states the result as a JSON record. The row draws its output, not the record.
    expect(await chatText(native.page)).not.toContain('"exitCode"')
  })
})

ampTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  await exerciseShellToolExecution(native, { includeFailure: false, prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
