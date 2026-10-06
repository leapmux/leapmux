import { expect } from '@playwright/test'

import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, assistantBubbles, toolRows, transcriptRows } from '../helpers/ui'
import { runWithoutApprovals } from './toolScenarios'

codewhaleTest.describe('Codewhale tool execution', () => {
  codewhaleTest('runs a command and draws its output', async ({ native }) => {
    const { page } = native
    await runWithoutApprovals(page)
    // The command text states no `codewhale-42`, so only the command's own output
    // can put it in a tool row. A command that printed its own text would match the
    // row's header whether or not the output reached the page.
    await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'echo-call', 'echo "codewhale-$((40 + 2))"')],
      prompt: 'Run the arithmetic command and report what it printed.',
      answer: 'The command printed its number.',
    })

    await expect(toolRows(page).filter({ hasText: 'codewhale-42' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The command printed its number.' })).toBeVisible()
  })

  codewhaleTest('draws the error of a command that fails', async ({ native }) => {
    const { page } = native
    await runWithoutApprovals(page)
    // A listing of a path that does not exist, which `ls` refuses. The runtime
    // fails the call and states the command's own error, with no exit code.
    await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'ls-call', 'ls codewhale-missing-path')],
      prompt: 'List codewhale-missing-path and report the result.',
      answer: 'The listing failed.',
    })

    await expect(toolRows(page).filter({ hasText: 'ls codewhale-missing-path' }).first()).toBeVisible()
    const failure = transcriptRows(page).filter({ hasText: 'No such file or directory' }).first()
    await expect(failure).toContainText('Error')
    await expect(failure).toContainText('codewhale-missing-path')
    await expect(assistantBubbles(page).filter({ hasText: 'The listing failed.' })).toBeVisible()
  })
})

codewhaleTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  await exerciseShellToolExecution(native, { includeFailure: false, prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
