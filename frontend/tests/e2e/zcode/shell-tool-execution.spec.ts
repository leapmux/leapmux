import { expect } from '@playwright/test'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, toolRows } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('a bash command renders as a tool card with its output', async ({ native }) => {
  // No scripted text states `zcode-42`, so only the command's own output can put
  // it in a tool row.
  //
  // ZCode's Build mode must run the command without approval. The arithmetic
  // form of the other providers' specs, `echo "zcode-$((40 + 2))"`, does not
  // qualify. ZCode's read-only check (`isRuntimeReadOnlyBashCommand` in
  // `zcode.cjs`) rejects every word that expands:
  //
  // - `$((...))`
  // - `$VAR`
  // - `$(...)`
  //
  // Build mode then gives the command the default Bash risk, `high`, and waits
  // for an approval that this test never gives. A `printf` with a literal
  // format and a numeric argument passes the check. So the turn clicks nothing,
  // and an approval request would hold it.
  await runNativeToolTurn(native, {
    toolCalls: [bashToolCall(native.provider, 'printf-call', `printf 'zcode-%d' 42`)],
    prompt: 'Run the printf command and show me the output.',
    answer: 'The command printed its number.',
    permissions: 'none',
  })

  await expect(toolRows(native.page).filter({ hasText: 'zcode-42' }).first()).toBeVisible()
})

zcodeTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  await exerciseShellToolExecution(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
})
