import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { ZCODE_MODE } from '../../../src/generated/contracts/zcode-protocol'
import { currentNativeAgent, expectNativeOptionValue, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { answerControl, expectNoControlBanner, expectSettingsChip, messageContents, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

/** Prove a native permission decision through a real removal in the private workspace. */
export async function exerciseZCodeRemovalPermission(context: ManagedNativeScenarioContext, options: { bypass: boolean }): Promise<void> {
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The ZCode permission proof requires a private working directory.')
  const suffix = uniqueMarker()
  const path = join(agent.workingDir, `zcode-permission-${suffix}.txt`)
  const content = 'Keep the private permission fixture.\n'
  const output = `ZCODEREMOVAL${suffix}42`
  const command = `rm -rf ${quotePosixShellArgument(path)} && printf 'ZCODEREMOVAL${suffix}%s' "$((40 + 2))"`
  writeFileSync(path, content)
  const callId = `zcode-removal-${suffix}`
  const start = await context.modelScript.queue(
    { toolCalls: [bashToolCall(context.provider, callId, command)] },
    nativeTextStep(context, options.bypass ? 'The command ran.' : 'I stopped at the confirmation.'),
  )
  context.modelScript.allowUnconsumed('The native runtime can end the permission turn before another model request.')
  await sendMessage(context.page, context.modelScript.prompt('Run the supplied native removal and keep its permission decision.'))
  const banner = await waitForControlBanner(context.page)
  await expect(banner).toContainText('Bash')
  await expect(banner).toContainText(basename(path))
  if (options.bypass) {
    const pills = context.page.getByRole('radiogroup', { name: 'Permissions' })
    await expect(pills.getByRole('radio', { name: 'Unchanged' })).toBeChecked()
    const bypass = pills.getByRole('radio', { name: 'Bypass' })
    await bypass.click()
    await expect(bypass).toBeChecked()
    await answerControl(context.page, 'allow')
    await waitForAgentIdle(context.page)
    await expectSettingsChip(context.page, 'Yolo')
    await expectNativeOptionValue(context, 'permissionMode', ZCODE_MODE.Yolo)
    // The output proves that the command ran: `printf` runs only after `rm` succeeded. The idle wait above cannot
    // prove it, because the indicator can clear between the permission answer and the tool run, so the file check
    // comes after the output.
    await expect(messageContents(context.page).filter({ hasText: output }).first()).toBeVisible()
    expect(existsSync(path)).toBe(false)
    // ZCode can end the turn after the tool runs, before it asks the model again. So the follow-up request is
    // optional, and the scenario reads its result only when the request exists.
    const followUp = (await context.modelScript.status()).requests.find(request => request.stepIndex === start + 1)
    if (followUp)
      expect(nativeToolResult(followUp, callId)).toContain(output)
  }
  else {
    await answerControl(context.page, 'deny')
    await waitForAgentIdle(context.page)
    expect(readFileSync(path, 'utf8')).toBe(content)
  }
  await expectNoControlBanner(context.page)
}
