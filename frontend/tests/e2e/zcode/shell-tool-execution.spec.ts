import { expect } from '@playwright/test'
import { exerciseShellToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { toolRows } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { bypassToolRequests } from './scenarios'

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
  //
  // The output check is the proof that Build mode ran the command. The shared
  // scenario below cannot carry this claim: it runs under the Yolo preset, and
  // its command writes a file, which the read-only check refuses.
  await runNativeToolTurn(native, {
    toolCalls: [bashToolCall(native.provider, 'printf-call', `printf 'zcode-%d' 42`)],
    prompt: 'Run the printf command and show me the output.',
    answer: 'The command printed its number.',
    permissions: 'none',
  })

  await expect(toolRows(native.page).filter({ hasText: 'zcode-42' }).first()).toBeVisible()
})

zcodeTest('keeps actual native shell output and a failed command result', async ({ native }) => {
  // ZCode begins the result of a failed command with an `Exit code N` line. The row header states the code, and the
  // body draws only the output. No other ZCode test reloads a `Bash` row: its output-path test uses the workflow route.
  await exerciseShellToolExecution(native, { prepare: () => bypassToolRequests(native), absentRowText: ['Exit code'], reload: true })
})
