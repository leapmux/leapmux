import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

zcodeTest('a bash command renders as a tool card with its output', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  // The command text, the prompt and the reply state no `zcode-42`, so only the
  // command's own output can put it in a tool row.
  //
  // ZCode's Build mode must run the command without approval. The arithmetic
  // form of the other providers' specs, `echo "zcode-$((40 + 2))"`, does not
  // qualify. ZCode's read-only check (`isRuntimeReadOnlyBashCommand` in
  // `zcode.cjs`) rejects every word that expands: `$((...))`, `$VAR` and `$(...)`.
  // Build mode then gives the command the default Bash risk, `high`, and waits
  // for an approval that this test never gives. A `printf` with a literal
  // format and a numeric argument passes the check.
  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.ZCODE, 'printf-call', `printf 'zcode-%d' 42`)] },
    { text: 'The command printed its number.' },
  )
  await sendMessage(page, modelScript.prompt('Run the printf command and show me the output.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)

  await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'zcode-42' }).first()).toBeVisible()
})

zcodeTest('keeps actual native shell output and a failed command result', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseShellToolExecution(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
})
