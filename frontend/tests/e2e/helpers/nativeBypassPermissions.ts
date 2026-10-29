import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { ManagedNativeScenarioContext, NativeAnswerStep, NativeToolOutcome } from './nativeScenario'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { expectNoNativeControl } from './nativeControlObservation'
import { assertPrivateNativePath } from './nativePrivatePath'
import { currentNativeAgent, nativeTextStep, nativeToolOutcome, toolTurnSteps } from './nativeScenario'
import { bashToolCall } from './providerToolCalls'
import { getGlobalState } from './server'
import { printfMarkerCommand, quotePosixShellArgument } from './shellArguments'
import { applyPermissionPreset, assistantBubbles, messageBubbles, sendMessage, waitForAgentIdle, waitForNativeSettingsHydrated, waitForSettingsIdle } from './ui'

/** A native command and the proof of its result. */
export interface NativeShortcutCommand {
  command: string
  resultProof: (outcome: NativeToolOutcome) => void | Promise<void>
}

/** The optional checks and command for a permission shortcut test. */
export interface NativeShortcutProof {
  /** Where the native exchange carries its answer. Supply context.readToolResult when results arrive outside model requests. */
  answerStep?: NativeAnswerStep
  /** Bring the session to the state that the shortcut must change, before the shortcut applies. */
  prepare?: () => Promise<void>
  /** Prove the native settings of the agent before each pass of the command. */
  settingsProof?: (agent: AgentInfo) => void | Promise<void>
  /** Supply a provider-specific command and its result proof for each pass. */
  commandProof?: (target: string, pass: number) => NativeShortcutCommand
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
    await expectNoNativeControl(context, {
      testId: 'control-banner',
      relatedProof: async () => {
      // One agent session runs both passes. Each pass uses a distinct tool call ID and assistant answer.
      // The default command produces a distinct output marker also.
      // Each pass recreates the target, so the first removal cannot satisfy the second check.
        const pass = Number(reload) + 1
        const id = `native-${preset}-${Number(reload)}`
        const output = `${outputPrefix}-${pass}-42`
        const answer = `The native ${preset} command completed in pass ${pass}.`
        const proof = options.commandProof?.(target, pass) ?? {
          command: `rm -rf ${quotePosixShellArgument(target)}; ${printfMarkerCommand(`${outputPrefix}-${pass}-`, 42)}`,
          resultProof: async (outcome: NativeToolOutcome) => {
            expect(outcome.text).toContain(output)
            // The command computes the output, so only execution can produce the complete marker.
            await expect(messageBubbles(context.page).filter({ hasText: output }).first()).toBeVisible()
          },
        }
        const steps = toolTurnSteps(
          [
            bashToolCall(context.provider, id, proof.command),
          ],
          nativeTextStep(context, answer),
          options.answerStep,
        )
        const start = await context.modelScript.queue(...steps)
        await sendMessage(context.page, context.modelScript.prompt('Run the scripted removal under the current permission shortcut.'))
        await context.modelScript.waitForSteps(start + steps.length)
        await waitForAgentIdle(context.page)
        expect(existsSync(target), 'The native command must remove its target.').toBe(false)
        const request = await context.modelScript.requestAt(start + steps.length - 1)
        await proof.resultProof(await nativeToolOutcome(context, request, id))
        await expect(assistantBubbles(context.page).filter({ hasText: answer }).first()).toBeVisible()
      },
    })
  }
}
