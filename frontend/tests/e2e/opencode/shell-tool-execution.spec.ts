import { expect } from '@playwright/test'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { railedRows } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'
import { readOpenCodeShellOutcome } from './nativeShellOutcome'

opencodeTest('tool call renders with span', async ({ native }) => {
  // Script the native ls call so the case requires actual tool execution.
  await runNativeToolTurn(native, {
    toolCalls: [bashToolCall(native.provider, 'ls-call', 'ls')],
    prompt: 'Use your shell tool to run `ls` in the current directory and report the output.',
    answer: 'The directory listing is above.',
  })

  // The native call must render a row with at least one span rail.
  await expect(railedRows(native.page).first()).toBeVisible()
})

opencodeTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  // OpenCode 1.18.34 ends the output reader's scope when the shell exits (ShellTool.run).
  // Hold the shell until the browser shows its output, so the reader keeps the bytes before its scope ends.
  await exerciseShellToolExecution(
    { ...native, readToolResult: (request, callId) => readOpenCodeShellOutcome(native, request, callId) },
    { outputGate: true },
  )
})
