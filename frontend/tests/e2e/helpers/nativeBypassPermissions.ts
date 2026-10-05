import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { expectNoNativeControl } from './nativeControlObservation'
import { assertPrivateNativePath } from './nativeCredentialIsolation'
import { currentNativeAgent, nativeTextStep } from './nativeScenario'
import { nativeToolResult } from './nativeToolResult'
import { bashToolCall } from './providerToolCalls'
import { getGlobalState } from './server'
import { printfMarkerCommand, quotePosixShellArgument } from './shellArguments'
import { applyPermissionPreset, sendMessage, waitForAgentIdle, waitForNativeSettingsHydrated, waitForSettingsIdle } from './ui'

interface NativeShortcutProof {
  settingsProof?: (agent: AgentInfo) => void | Promise<void>
}

/** Prove Bypass changes native permission behavior before and after reload. */
export async function exerciseBypassPermissions(context: ManagedNativeScenarioContext, options: NativeShortcutProof = {}): Promise<void> {
  await exercisePermissionShortcut(context, 'bypass', options)
}

/** Prove Smart uses its native safety preset before and after reload. */
export async function exerciseSmartPermissions(context: ManagedNativeScenarioContext, options: NativeShortcutProof = {}): Promise<void> {
  await exercisePermissionShortcut(context, 'smart', options)
}

/** Run the same private native command through either actual permission shortcut. */
async function exercisePermissionShortcut(context: ManagedNativeScenarioContext, preset: 'smart' | 'bypass', options: NativeShortcutProof): Promise<void> {
  const agent = await currentNativeAgent(context)
  assertPrivateNativePath(agent.workingDir, getGlobalState().tmpDir)
  const target = join(agent.workingDir, `native-${preset}-proof`)
  const outputPrefix = preset.toUpperCase()
  await applyPermissionPreset(context.page, preset)
  await waitForSettingsIdle(context.page)
  for (const reload of [false, true]) {
    mkdirSync(target)
    writeFileSync(join(target, 'keep.txt'), `Remove only through the isolated native ${preset} command.\n`)
    assertPrivateNativePath(target, getGlobalState().tmpDir)
    if (reload) {
      await context.page.reload()
      await waitForNativeSettingsHydrated(context.page)
    }
    await options.settingsProof?.(await currentNativeAgent(context))
    await expectNoNativeControl(context, { testId: 'control-banner', relatedControl: async () => {
      const start = (await context.modelScript.status()).stepCount
      const id = `native-${preset}-${start}`
      await context.modelScript.queue(
        { toolCalls: [bashToolCall(context.provider, id, `rm -rf ${quotePosixShellArgument(target)}; ${printfMarkerCommand(outputPrefix, 42)}`)] },
        nativeTextStep(context, `The native ${preset} command completed.`),
      )
      await sendMessage(context.page, context.modelScript.prompt('Run the scripted removal under the current permission shortcut.'))
      const status = await context.modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(context.page)
      expect(existsSync(target)).toBe(false)
      const request = status.requests.find(record => record.stepIndex === start + 1)
      if (!request)
        throw new Error('The native permission result reached no following model request.')
      const result = context.readToolResult ? await context.readToolResult(request, id) : { text: nativeToolResult(request, id) }
      expect(result.text).toContain(`${outputPrefix}42`)
    } })
  }
}
