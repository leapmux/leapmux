import { expect } from '@playwright/test'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, chatText } from '../helpers/ui'

import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The installed agent executes the scripted shell command. Calculated output proves that the executor ran the command.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.describe('Oh My Pi tool execution', () => {
  ohMyPiTest('draws the output of a command', async ({ native }) => {
    // The command text states no `omp-42`, so only the command's own output
    // can put it on the page.
    await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'echo-call', 'echo "omp-$((40 + 2))"')],
      prompt: 'Run the arithmetic command.',
      answer: 'The command printed its number.',
    })

    await expect.poll(() => chatText(native.page)).toContain('omp-42')
    // omp appends a `Wall time: <n> seconds` notice to every output. The
    // extractor drops it, because the row already states how the call ended.
    expect(await chatText(native.page)).not.toContain('Wall time:')
  })
})

ohMyPiTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  await exerciseShellToolExecution(native, { includeFailure: false, prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
