import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { expectNoNativeControl } from './nativeControlObservation'
import { assertPrivateNativePath } from './nativePrivatePath'
import { currentNativeAgent, nativeTextStep, nativeToolOutcome } from './nativeScenario'
import { bashToolCall } from './providerToolCalls'
import { getGlobalState } from './server'
import { printfMarkerCommand, quotePosixShellArgument } from './shellArguments'
import { applyPermissionPreset, assistantBubbles, messageBubbles, sendMessage, waitForAgentIdle, waitForNativeSettingsHydrated, waitForSettingsIdle } from './ui'

/** What a permission shortcut proof runs around its native command. */
export interface NativeShortcutProof {
  /** Bring the session to the state that the shortcut must change, before the shortcut applies. */
  prepare?: () => Promise<void>
  /** Prove the native settings of the agent before each pass of the command. */
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
  await options.prepare?.()
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
    await expectNoNativeControl(context, { testId: 'control-banner', relatedProof: async () => {
      // One agent session runs both passes. Each pass gives its tool call, its output, and its answer their own text,
      // so a check of the second pass cannot pass on what the first pass shows.
      const pass = Number(reload) + 1
      const id = `native-${preset}-${Number(reload)}`
      const output = `${outputPrefix}-${pass}-42`
      const answer = `The native ${preset} command completed in pass ${pass}.`
      const start = await context.modelScript.queue(
        { toolCalls: [bashToolCall(context.provider, id, `rm -rf ${quotePosixShellArgument(target)}; ${printfMarkerCommand(`${outputPrefix}-${pass}-`, 42)}`)] },
        nativeTextStep(context, answer),
      )
      await sendMessage(context.page, context.modelScript.prompt('Run the scripted removal under the current permission shortcut.'))
      await context.modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(context.page)
      expect(existsSync(target)).toBe(false)
      const request = await context.modelScript.requestAt(start + 1)
      expect((await nativeToolOutcome(context, request, id)).text).toContain(output)
      // The command computes no part of the output in its text, so only the run of the command shows it.
      await expect(messageBubbles(context.page).filter({ hasText: output }).first()).toBeVisible()
      await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
    } })
  }
}
