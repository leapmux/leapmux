import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { toolRows } from '../helpers/ui'
import { codexExecContext } from './scenarios'

codexTest.describe('codex tool execution', () => {
  codexTest('command execution shows command, output, and exit code', async ({ native }) => {
    // The command really runs, so the exit code the card shows is the shell's own.
    // The command text states no `codex-hello-42` and no `codex-done-55`, so only
    // the command's own output can put them in a tool row.
    await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'exit-call', `sh -c 'echo "codex-hello-$((40 + 2))"; echo "codex-done-$((50 + 5))"; exit 7'`)],
      prompt: 'Run this exact command and report its result.',
      answer: 'The command exited with status 7.',
    })

    const toolMessages = toolRows(native.page)
    await expect(toolMessages.filter({ hasText: 'codex-hello-42' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'codex-done-55' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'Error (exit 7)' }).first()).toBeVisible()
  })
})

codexTest('returns native shell success and failure output to the following model request', async ({ native }) => {
  await exerciseShellToolExecution(codexExecContext(native))
})
