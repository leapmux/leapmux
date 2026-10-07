import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { ZCODE_MODE } from '../../../src/generated/contracts/zcode-protocol'
import { stepRequest } from '../helpers/mockModelScript'
import { currentNativeAgent, expectNativeOptionValue, nativeOptionValue, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { answerControl, expectNoControlBanner, expectSettingsChip, messageContents, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

/**
 * How the reader answers the permission request of {@link exerciseZCodeRemovalPermission}:
 *
 * - `{ bypass: false, decision }`: Allow or Deny under the Unchanged pill. The answer keeps the permission mode.
 * - `{ bypass: true }`: Allow with the Bypass pill selected. The answer switches the session to Yolo.
 */
export type ZCodeRemovalAnswer = { bypass: false, decision: 'allow' | 'deny' } | { bypass: true }

/** Prove a native permission decision through a real removal in the private workspace. */
export async function exerciseZCodeRemovalPermission(context: ManagedNativeScenarioContext, answer: ZCodeRemovalAnswer): Promise<void> {
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The ZCode permission proof requires a private working directory.')
  const modeBefore = nativeOptionValue(agent, 'permissionMode')
  if (modeBefore === undefined)
    throw new Error('The ZCode permission proof requires the permission mode of the session.')
  const allowed = answer.bypass || answer.decision === 'allow'
  const suffix = uniqueMarker()
  const path = join(agent.workingDir, `zcode-permission-${suffix}.txt`)
  const content = 'Keep the private permission fixture.\n'
  const output = `ZCODEREMOVAL${suffix}42`
  const command = `rm -rf ${quotePosixShellArgument(path)} && printf 'ZCODEREMOVAL${suffix}%s' "$((40 + 2))"`
  writeFileSync(path, content)
  const callId = `zcode-removal-${suffix}`
  const start = await context.modelScript.queue(
    { toolCalls: [bashToolCall(context.provider, callId, command)] },
    nativeTextStep(context, allowed ? 'The command ran.' : 'I stopped at the confirmation.'),
  )
  context.modelScript.allowUnconsumed('The native runtime can end the permission turn before another model request.')
  await sendMessage(context.page, context.modelScript.prompt('Run the supplied native removal and keep its permission decision.'))
  const banner = await waitForControlBanner(context.page)
  await expect(banner).toContainText('Bash')
  await expect(banner).toContainText(basename(path))
  if (!allowed) {
    await answerControl(context.page, 'deny')
    await waitForAgentIdle(context.page)
    expect(readFileSync(path, 'utf8')).toBe(content)
    await expectNoControlBanner(context.page)
    // ZCode's deny option carries ZCode's own refusal to the model, and the saved row states it on a line of its own.
    await expect(savedControlAnswer(context.page)).toHaveText(/^Deny\nThe user doesn't want to proceed with this tool use\./)
    return
  }
  const pills = context.page.getByRole('radiogroup', { name: 'Permissions' })
  await expect(pills.getByRole('radio', { name: 'Unchanged' })).toBeChecked()
  if (answer.bypass) {
    const bypass = pills.getByRole('radio', { name: 'Bypass' })
    await bypass.click()
    await expect(bypass).toBeChecked()
  }
  await answerControl(context.page, 'allow')
  await waitForAgentIdle(context.page)
  await expect(savedControlAnswer(context.page)).toHaveText('Allow')
  if (answer.bypass) {
    await expectSettingsChip(context.page, 'Yolo')
    await expectNativeOptionValue(context, 'permissionMode', ZCODE_MODE.Yolo)
  }
  else {
    // The Unchanged pill answers this one request and keeps the mode of the session.
    await expectNativeOptionValue(context, 'permissionMode', modeBefore)
  }
  // The output proves that the command ran: `printf` runs only after `rm` succeeded. The idle wait above cannot
  // prove it, because the indicator can clear between the permission answer and the tool run, so the file check
  // comes after the output.
  await expect(messageContents(context.page).filter({ hasText: output }).first()).toBeVisible()
  expect(existsSync(path)).toBe(false)
  // ZCode can end the turn after the tool runs, before it asks the model again. So the follow-up request is
  // optional, and the scenario reads its result only when the agent consumed the follow-up step.
  const status = await context.modelScript.status()
  if (status.nextStep > start + 1)
    expect(nativeToolResult(stepRequest(status, start + 1), callId)).toContain(output)
  await expectNoControlBanner(context.page)
}
