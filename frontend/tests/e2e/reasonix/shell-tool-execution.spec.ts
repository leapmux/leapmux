import { expect } from '@playwright/test'
import { exerciseShellToolExecution, expectShellToolRows, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { printfMarkerCommand, uniqueMarker } from '../helpers/shellArguments'
import { toolCallRow } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { bypassToolRequests } from './scenarios'

reasonixTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  await exerciseShellToolExecution(native, { prepare: () => bypassToolRequests(native), absentRowText: ['error: command exited', '[receipt r_'] })

  // A successful command can print the same first line as a native failure.
  // Its structured zero exit keeps that line as actual output.
  const prefix = 'error: command exited: exit status 7'
  const marker = uniqueMarker('PRINTED')
  const output = `${marker}42`
  const callId = `printed-exit-line-${marker}`
  const turn = await runNativeToolTurn(native, {
    toolCalls: [bashToolCall(native.provider, callId, `printf 'error: command exited: exit status %s\\n' 7; ${printfMarkerCommand(marker, 42)}`)],
    prompt: 'Print the literal exit-line text and its computed marker.',
    answer: 'The literal output command ended.',
  })
  expect(nativeToolResult(turn.resultRequest, callId)).toContain(`${prefix}\n${output}`)
  await expectShellToolRows(native, [{ printedPrefix: marker, output, exitCode: 0, exactOutput: `${prefix}\n${output}` }])
  await expect(toolCallRow(native.page, callId)).not.toContainText('Error (exit 7)')
})
