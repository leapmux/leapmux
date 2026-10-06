import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { expectNoControlBanner, expectSettingsChip, messageContents, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

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
  const start = (await context.modelScript.status()).stepCount
  await context.modelScript.queue(
    { toolCalls: [bashToolCall(context.provider, callId, command)] },
    { text: options.bypass ? 'The command ran.' : 'I stopped at the confirmation.' },
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
    await context.page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await waitForAgentIdle(context.page)
    await expectSettingsChip(context.page, 'Yolo')
    expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe('yolo')
    expect(existsSync(path)).toBe(false)
    await expect(messageContents(context.page).filter({ hasText: output }).first()).toBeVisible()
    const followUp = (await context.modelScript.status()).requests.find(request => request.stepIndex === start + 1)
    if (followUp)
      expect(nativeToolResult(followUp, callId)).toContain(output)
  }
  else {
    const deny = context.page.getByTestId('control-deny-btn').filter({ visible: true })
    await expect(deny).toBeVisible()
    await deny.click()
    await waitForAgentIdle(context.page)
    expect(readFileSync(path, 'utf8')).toBe(content)
  }
  await expectNoControlBanner(context.page)
}
