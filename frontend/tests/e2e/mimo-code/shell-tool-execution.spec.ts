import { expect } from '@playwright/test'
import { nativeTextStep } from '../helpers/nativeScenario'
import { runNativeToolTurn, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { createOutputGate, runWithGatedOutput } from '../helpers/outputGate'
import { bashToolCall, mimoInteractiveBashToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { applyPermissionPreset, messageContents, railedRows, sendMessage, toolRows } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'
import { exerciseMiMoShellToolExecution } from './shellToolExecution'

mimoTest.describe('MiMo Code tool execution', () => {
  mimoTest('a shell command renders as a tool card with its output', async ({ native }) => {
    // MiMo Code 0.1.15 can lose the output of a command that exits right after it
    // writes, and the tool then returns "(no output)". The gate keeps the command alive
    // until the running row shows the output (see `OutputGate`), so the output cannot be
    // lost. MiMo publishes that row from the fiber that reads the output, after the fiber
    // keeps the bytes.
    const gate = createOutputGate(createTestDirectory('mimo-shell-gate-'))
    const command = gate.hold('echo "mimo-$((40 + 2))"')
    // No scripted text states `mimo-42`, so only the command's own output can put
    // it in a tool row.
    const outputRow = () => toolRows(native.page).filter({ hasText: 'mimo-42' }).first()
    const start = await native.modelScript.queue(
      { toolCalls: [bashToolCall(native.provider, 'echo-call', command)] },
      nativeTextStep(native, 'The command printed its number.'),
    )
    await sendMessage(native.page, native.modelScript.prompt('Run the arithmetic command.'))
    await runWithGatedOutput(
      { gate, shown: () => expect(outputRow(), 'the running row shows the output of the held command').toBeVisible() },
      () => waitForNativeToolSteps(native, start + 2),
    )

    // The output reaches the card from MiMo's own metadata, not from the model's
    // reply, which does not repeat it.
    await expect(outputRow()).toBeVisible()
    await expect(railedRows(native.page).first()).toBeVisible()
  })
})

mimoTest.describe('MiMo Code interactive commands', () => {
  // Nobody can type into a command that LeapMux runs, so the worker refuses the
  // request at once. The turn goes on: the model reads the refusal as the
  // command's output and answers.
  //
  // The claim is that no approval request appears, so the turn clicks nothing and
  // requires that no banner shows.
  mimoTest('refuses an interactive command without blocking the turn', async ({ native }) => {
    const { page } = native
    await runNativeToolTurn(native, {
      toolCalls: [mimoInteractiveBashToolCall('interactive-call', 'read -p "Name? " name; echo "hi $name"')],
      prompt: 'Ask for my name in the shell.',
      answer: 'INTERACTIVE_REFUSED',
      permissions: 'none',
    })

    await expect(messageContents(page).filter({ hasText: 'LeapMux cannot run an interactive command' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'INTERACTIVE_REFUSED' }).first()).toBeVisible()
  })
})

mimoTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  // The failed command writes only to stderr, so its output gate releases on the stderr text that the running row shows.
  await exerciseMiMoShellToolExecution(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
