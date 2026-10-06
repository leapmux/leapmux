import { expect } from '@playwright/test'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectSettingsChip, toolRows, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('uses Kimi Code tools', () => {
  // Always Ask stops every command and edit at a banner. These cases assert
  // what a tool DRAWS, so Never Ask removes the banner from the turn. The
  // control-request spec covers the banner itself.
  //
  // Each command prints a number that its own text does not state, such as
  // `kimi-test-output-42` from `kimi-test-output-$((40 + 2))`. The row's header
  // shows the command, so a marker that the command text holds matches the row
  // whether or not the command ran.
  kimiTest.beforeEach(async ({ native }) => {
    await waitForSettingsHydrated(native.page)
    await applyPermissionPreset(native.page, 'bypass')
    await expectSettingsChip(native.page, 'Never Ask')
  })

  // The shared scenario at the end of this file never reloads the page, so
  // this test holds the reload claim. The check before the reload is the
  // first half of that claim: the row that the reload must keep.
  kimiTest('a command renders as a tool card with its output, and keeps it after a reload', async ({ native }) => {
    await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'echo-call', 'echo "kimi-test-output-$((40 + 2))"')],
      prompt: 'Run the echo command and report the output.',
      answer: 'The command printed its output.',
    })

    const toolMessages = toolRows(native.page)
    await expect(toolMessages.filter({ hasText: 'kimi-test-output-42' }).first()).toBeVisible()

    await native.page.reload()
    await expect(toolMessages.filter({ hasText: 'kimi-test-output-42' }).first()).toBeVisible()
  })

  // Kimi Code states a failed command's exit code only as a trailer on the
  // output, and the plugin reads it from there. The shared scenario at the
  // end of this file runs no failed command for Kimi Code
  // (`includeFailure: false`), so this test holds the only failure proof.
  kimiTest('a failed command shows its output and its exit code', async ({ native }) => {
    await runNativeToolTurn(native, {
      toolCalls: [bashToolCall(native.provider, 'exit-call', `sh -c 'echo "hello-from-kimi-$((40 + 2))"; exit 7'`)],
      prompt: 'Run this exact command and report its result.',
      answer: 'The command exited with status 7.',
    })

    const toolMessages = toolRows(native.page)
    await expect(toolMessages.filter({ hasText: 'hello-from-kimi-42' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'Error (exit 7)' }).first()).toBeVisible()
    // The trailer is a fact the card states, not output it repeats.
    await expect(toolMessages.filter({ hasText: 'Command failed with exit code' })).toHaveCount(0)
  })
})

kimiTest('preserves a literal private shell path with spaces and metacharacters', async ({ native }) => {
  await exerciseShellToolExecution(native, { includeFailure: false, prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
