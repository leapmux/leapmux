import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { createOutputGate, runWithGatedOutput } from '../helpers/outputGate'
import { bashToolCall } from '../helpers/providerToolCalls'
import { printfMarkerCommand, quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { toolRows } from '../helpers/ui'
import { bypassToolRequests } from './scenarios'

copilotTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  // GitHub Copilot ends each result with `<shellId: N completed with exit code N>`. The row header states a nonzero
  // code, and the body draws only the output.
  await exerciseShellToolExecution(native, {
    prepare: () => bypassToolRequests(native),
    absentRowText: ['<shellId:', 'completed with exit code'],
  })
})

copilotTest('keeps both output segments visible while the native shell still runs', async ({ native }) => {
  await bypassToolRequests(native)
  const agent = await currentNativeAgent(native)
  const directory = createNativeToolDirectory(agent.workingDir)
  const first = createOutputGate(directory)
  const second = createOutputGate(directory)
  const marker = uniqueMarker('CHUNKS')
  const firstOutput = `${marker}42`
  const secondOutput = `${marker}77`
  const command = second.hold(`sh -c ${quotePosixShellArgument(first.hold(printfMarkerCommand(marker, 42)))}; ${printfMarkerCommand(marker, 77)}`)
  const liveOutput = toolRows(native.page).locator('[data-tool-output-preview]')

  await runWithGatedOutput({
    gate: first,
    shown: async () => expect(liveOutput.filter({ hasText: firstOutput }).first()).toBeVisible(),
  }, () => runWithGatedOutput({
    gate: second,
    shown: async () => {
      const output = liveOutput.filter({ hasText: secondOutput }).first()
      await expect(output).toBeVisible()
      await expect(output).toContainText(firstOutput)
    },
  }, () => runNativeToolTurn(native, {
    toolCalls: [
      bashToolCall(native.provider, 'copilot-live-chunks', command),
    ],
    prompt: 'Run the command with two controlled output segments.',
    answer: 'Both native output segments completed.',
    permissions: 'none',
  })))
})
