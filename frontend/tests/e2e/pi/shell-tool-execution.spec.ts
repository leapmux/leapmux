import { expect } from '@playwright/test'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { toolRows } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

piTest('bash command execution renders output in chat', async ({ native }) => {
  // No scripted text states `pi-42`, so only the command's own output can put it
  // in a tool row.
  await runNativeToolTurn(native, {
    toolCalls: [bashToolCall(native.provider, 'echo-call', 'echo "pi-$((40 + 2))"')],
    prompt: 'Run the arithmetic command and show me the output.',
    answer: 'The command printed its number.',
  })

  await expect(toolRows(native.page).filter({ hasText: 'pi-42' }).first()).toBeVisible()
})

piTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  await exerciseShellToolExecution(native)
})
