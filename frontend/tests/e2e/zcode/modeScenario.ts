import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, nativeOptionValue, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { chooseSettingsOption, expectNoControlBanner, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

/** Prove the selected native ZCode mode through its actual mutation and permission path. */
export async function exerciseZCodeMode(context: ManagedNativeScenarioContext, mode: 'plan' | 'yolo' | 'build'): Promise<void> {
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native ZCode mode proof requires a working directory.')
  expect(nativeOptionValue(agent, 'permissionMode')).toBe(mode)
  const suffix = uniqueMarker()
  const path = join(agent.workingDir, `zcode-mode-write-${suffix}.txt`)
  const callId = `zcode-${mode}-write-${suffix}`
  const toolCall = writeToolCall(context.provider, callId, { path, content: `${mode} mutation\n` })
  if (mode === 'build') {
    // Build mode asks before a write. A denial keeps the file absent, and ZCode returns the refusal to the model.
    await exerciseNativePermissionDecision(context, {
      toolCall,
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText(basename(path)),
      nativeProof: (request) => {
        expect(existsSync(path)).toBe(false)
        // The model reads one result for the refused call. The reader throws when the request holds none.
        nativeToolResult(request, callId)
      },
    })
    await expectNoControlBanner(context.page)
    return
  }
  // Plan and Yolo modes raise no permission request, so the turn must end with no click. A banner would hold the
  // turn, and the step wait would fail.
  const start = await context.modelScript.queue({ toolCalls: [toolCall] }, nativeTextStep(context, `The ${mode} check ended.`))
  await sendMessage(context.page, context.modelScript.prompt(`Try the scripted write in ${mode} mode.`))
  await context.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(context.page)
  await expectNoControlBanner(context.page)
  const result = nativeToolResult(await context.modelScript.requestAt(start + 1), callId)
  if (mode === 'yolo') {
    expect(readFileSync(path, 'utf8')).toBe(`${mode} mutation\n`)
    expect(result).toContain(basename(path))
  }
  else {
    expect(existsSync(path)).toBe(false)
    expect(result).toMatch(/plan|not available|denied/i)
  }
}

/**
 * Prove that Plan mode refuses a native write that Yolo mode runs, and that each mode keeps its behavior after a
 * reload. Each mode runs one write before the reload and one after it.
 */
export async function exerciseZCodePlanAndYolo(context: ManagedNativeScenarioContext): Promise<void> {
  for (const mode of ['plan', 'yolo'] as const) {
    await chooseSettingsOption(context.page, `permissionMode-${mode}`)
    await waitForSettingsIdle(context.page)
    await exerciseZCodeMode(context, mode)
    await context.page.reload()
    await expectSettingsOptionChosen(context.page, `permissionMode-${mode}`)
    await exerciseZCodeMode(context, mode)
  }
}
