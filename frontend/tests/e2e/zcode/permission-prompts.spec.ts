import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionReason, exerciseRememberedAllow } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { savedControlAnswer, toolRows } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeRemovalPermission } from './permissionScenario'

zcodeTest('a risky command produces a permission banner that can be denied', async ({ native }) => {
  await exerciseZCodeRemovalPermission(native, { bypass: false, decision: 'deny' })
})

zcodeTest('an allow under the Unchanged pill runs the command and keeps the mode', async ({ native }) => {
  await exerciseZCodeRemovalPermission(native, { bypass: false, decision: 'allow' })
})

// The request offers "Always allow in this project", whose reply carries the
// permission-rule update that remembers the exact command for the project. The same
// command in the next turn must run with no banner at any time. The backend keeps the
// rule in its own sqlite store under the private home, and its ruleContent is this
// run's exact marker'd command, so no later spec can inherit the grant and no rule
// file needs restoring.
zcodeTest('a project-scoped allow covers the same command in the next turn', async ({ native }) => {
  const workingDir = (await currentNativeAgent(native)).workingDir
  const suffix = uniqueMarker()
  const file = join(workingDir, `zcode-project-${suffix}.txt`)
  writeFileSync(file, 'first\n')
  const command = `rm -rf ${quotePosixShellArgument(file)} && printf 'ZCODEPROJECT%s' "$((40 + 2))"`
  const ran = () => expect(existsSync(file)).toBe(false)
  await exerciseRememberedAllow(native, {
    scope: 'Project',
    firstCall: bashToolCall(native.provider, `zcode-project-first-${suffix}`, command),
    secondCall: bashToolCall(native.provider, `zcode-project-second-${suffix}`, command),
    beforeDecision: () => expect(existsSync(file)).toBe(true),
    firstProof: ran,
    secondProof: ran,
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow'),
  })
})

// The reason rides in the `reason` of the deny option's own response, and ZCode hands it to the model.
zcodeTest('a typed refusal reason reaches the model with the denial', async ({ native }) => {
  const file = join((await currentNativeAgent(native)).workingDir, 'zcode-reason.txt')
  const content = 'Keep the private permission fixture.\n'
  writeFileSync(file, content)
  await exerciseNativePermissionReason(native, {
    toolCall: bashToolCall(native.provider, 'zcode-reason-removal', `rm -rf ${quotePosixShellArgument(file)}`),
    route: 'native-reply',
    beforeDecision: banner => expect(banner).toContainText('zcode-reason.txt'),
    expectNotRun: () => expect(readFileSync(file, 'utf8')).toBe(content),
    // The saved row states the decision, and the reason that the native reply carried on a line of its own.
    viewProof: reason => expect(savedControlAnswer(native.page)).toHaveText(`Deny\n${reason}`),
  })
})

// Build mode must run a read-only command without approval. The arithmetic form
// of the other providers' specs, `echo "zcode-$((40 + 2))"`, does not qualify:
// ZCode's read-only check (`isRuntimeReadOnlyBashCommand` in `zcode.cjs`)
// rejects every word that expands (`$((...))`, `$VAR`, `$(...)`), and Build mode
// then gives such a command the default Bash risk, `high`, and waits for an
// approval that this test never gives. A `printf` with a literal format and a
// numeric argument passes the check, so the turn clicks nothing and an approval
// request would hold it. No scripted text states `zcode-42`, so only the
// command's own output can put it in a tool row: that output is the proof that
// Build mode ran the command.
zcodeTest('Build mode runs a read-only command with no approval', async ({ native }) => {
  await runNativeToolTurn(native, {
    toolCalls: [bashToolCall(native.provider, 'printf-call', `printf 'zcode-%d' 42`)],
    prompt: 'Run the printf command and show me the output.',
    answer: 'The command printed its number.',
    permissions: 'none',
  })

  await expect(toolRows(native.page).filter({ hasText: 'zcode-42' }).first()).toBeVisible()
})
