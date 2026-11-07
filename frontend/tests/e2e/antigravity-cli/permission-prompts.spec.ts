import { randomUUID } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { antigravityTest } from '../antigravity-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { cssAttributeValue } from '../helpers/cssAttribute'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { createNativePermissionFileWrite, exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseNativePermissionWrite, expectDeclinedToolRow, expectDeclinedToolRowAcrossReload } from '../helpers/nativePermission'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { bashToolCall, spawnSubagentToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { openRunningNativeChild } from '../helpers/runningChildProof'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { answerControl, chooseSettingsOption, messageBubbles, openWorkspace, savedControlAnswer, toolCallRow, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { antigravityNativeToolResult, antigravityNativeToolResultContent } from './toolResult'

antigravityTest('requires a native permission decision before a real file change', async ({ native }) => {
  await exerciseNativePermissionWrite(native, {
    // The saved answer displays the native permission decision as Allow.
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Allow'),
  })
})

/**
 * Deny fails the tool and sends the refusal to the model as the function response of the same call.
 * The agent loop continues with that response, so the next model request carries it.
 */
antigravityTest('sends the exact native refusal after a Deny decision and keeps the file bytes', async ({ native }) => {
  const { page } = native
  const agent = await currentNativeAgent(native)
  const fileName = 'native-denied-write.txt'
  const file = join(agent.workingDir, fileName)
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  const callId = 'antigravity-denied-write'
  const refusal = 'denied permission'
  const operation = await createNativePermissionFileWrite(native, {
    fileName,
    callId,
    outputPrefix: 'UNAPPROVED_WRITE',
    initialContent,
  })
  await exerciseNativePermissionDecision(native, {
    toolCall: operation.toolCall,
    decision: 'deny',
    beforeDecision: operation.beforeDecision,
    nativeProof: (request) => {
      expect(readFileSync(file, 'utf8')).toBe(initialContent)
      expect(JSON.stringify(antigravityNativeToolResultContent(request, callId))).toContain(refusal)
    },
    // The model receives the typed refusal. The visible tool row carries Antigravity's generic native error.
    viewProof: async () => {
      await expect(savedControlAnswer(page)).toHaveText('Deny')
      await expectDeclinedToolRowAcrossReload(native, callId, 'permission check failed for command')
    },
  })
})

/**
 * The renderer reads Path and Content from the native tool-call arguments.
 * After denial, both rows keep the file path visible and hide the proposed content.
 * The result row displays the native refusal.
 */
antigravityTest('reads a denied file write as declined, with its file and no proposed text', async ({ native }) => {
  const { page } = native
  const agent = await currentNativeAgent(native)
  const fileName = 'native-denied-file-write.txt'
  const file = join(agent.workingDir, fileName)
  const initialContent = `KEEP_THE_NATIVE_FILE_${randomUUID()}\n`
  const proposed = `PROPOSED_NATIVE_TEXT_${randomUUID()}\n`
  writeFileSync(file, initialContent)
  const callId = 'antigravity-denied-file-write'
  const refusal = 'denied permission'
  const renderedCallId = callId
  const callRows = messageBubbles(page).and(page.locator(`[data-tool-call-id="${cssAttributeValue(renderedCallId)}"]`))
  await exerciseNativePermissionDecision(native, {
    toolCall: writeToolCall(native.provider, callId, { path: file, content: proposed }),
    decision: 'deny',
    beforeDecision: () => {
      expect(readFileSync(file, 'utf8')).toBe(initialContent)
    },
    nativeProof: (request) => {
      expect(readFileSync(file, 'utf8')).toBe(initialContent)
      expect(JSON.stringify(antigravityNativeToolResultContent(request, callId))).toContain(refusal)
    },
    viewProof: async () => {
      for (const reload of [false, true]) {
        if (reload) {
          await page.reload()
          await openWorkspace(page, native.workspaceId)
        }
        await expectDeclinedToolRow(page, renderedCallId, refusal)
        await expect(toolCallRow(page, renderedCallId)).toContainText(fileName)
        await expect(toolCallRow(page, renderedCallId, 'request')).toContainText(fileName)
        await expect(callRows.filter({
          hasText: proposed.trim(),
        })).toHaveCount(0)
      }
    },
  })
})

// Antigravity reads userDenyInstruction as the alternative instruction in the refused tool result.
antigravityTest('sends the reader\'s typed refusal reason in the native reply', async ({ native }) => {
  const file = join((await currentNativeAgent(native)).workingDir, 'native-reason-write.txt')
  await exerciseNativePermissionReason(native, {
    toolCall: bashToolCall(native.provider, 'antigravity-reason-write', `printf refused > ${quotePosixShellArgument(file)}`),
    route: 'native-reply',
    expectNotRun: () => expect(existsSync(file)).toBe(false),
    viewProof: () => expect(savedControlAnswer(native.page)).toHaveText('Deny'),
  })
})

antigravityTest('approves a native wake-turn command without another user prompt', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  const marker = uniqueMarker()
  const prompt = native.modelScript.prompt(`ANTIGRAVITYWAKECHILD${marker}`)
  const gate = `antigravity-wake-child-${marker}`
  const callId = `antigravity-wake-approval-${marker}`
  const spawn = spawnSubagentToolCall(native.provider, `antigravity-wake-spawn-${marker}`, { description: 'Wake the parent', prompt })
  const output = `ANTIGRAVITY_WAKE_OUTPUT_${marker}`
  const start = (await native.modelScript.status()).nextStep
  let child: Awaited<ReturnType<typeof openRunningNativeChild>> | undefined
  await withCleanup(async () => {
    child = await openRunningNativeChild(native, {
      spawn,
      gate,
      child: {
        matcher: { user: prompt },
        finalStep: { text: 'The wake child completed.' },
      },
      parentSteps: [
        {
          toolCalls: [spawn],
        },
        {
          toolCalls: [
            bashToolCall(native.provider, callId, `printf ${output}`),
          ],
        },
        { text: 'The native wake turn completed.' },
      ],
    })
    await native.modelScript.releaseGate(gate)
    const banner = await waitForControlBanner(native.page)
    await expect(banner).toContainText(`printf ${output}`)
    await answerControl(native.page, 'allow')
    expect(antigravityNativeToolResult(await native.modelScript.requestAt(start + 2), callId)).toContain(output)
  }, async () => {
    if (child)
      await child.finish()
    else
      await native.modelScript.releaseGateIfHeld(gate)
  })
})

antigravityTest('answers a permission owned by the native child cascade', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-default')
  await waitForSettingsIdle(native.page)
  const marker = uniqueMarker()
  const prompt = native.modelScript.prompt(`ANTIGRAVITYCHILDPERMISSION${marker}: run a command.`)
  const outputMarker = `ANTIGRAVITY_CHILD_PERMISSION_OUTPUT_${marker}`
  const gate = `antigravity-child-permission-${marker}`
  const callId = `antigravity-child-command-${marker}`
  // Antigravity keeps an internally spawned child's shell sandbox read-only even after approval.
  // Stdout proves that the approved command ran without depending on a sandbox file grant.
  const command = `printf '%s\\n' ${quotePosixShellArgument(outputMarker)}`
  await native.modelScript.fallback({ text: 'The parent acknowledged the child report.' })
  let child: Awaited<ReturnType<typeof openRunningNativeChild>> | undefined
  await withCleanup(async () => {
    const running = openRunningNativeChild(native, {
      spawn: spawnSubagentToolCall(native.provider, `antigravity-child-permission-spawn-${marker}`, { description: 'Run a command', prompt }),
      gate,
      child: {
        matcher: { user: prompt },
        finalStep: { text: 'The permitted child completed.' },
      },
      rules: [
        {
          name: 'the native child asks for command permission',
          when: { user: prompt },
          respond: {
            toolCalls: [
              bashToolCall(native.provider, callId, command),
            ],
          },
          once: true,
        },
      ],
    })
    const row = await requireRegistryRow(native.page)
    const childAgentId = await openChildTabFromRow(native.page, row)
    const watch = await watchNativeControls(native.leapmuxServer, childAgentId)
    try {
      const banner = await waitForControlBanner(native.page)
      await expect(banner).toContainText(command)
      const currentChild = await nativeAgentById(native, childAgentId)
      expect(currentChild?.agentSessionId).toBe(watch.controls().at(-1)?.payload.cascadeId)
      await answerControl(native.page, 'allow')
      await expect.poll(() => watch.controls().at(-1)?.responseState, JSON.stringify(watch.controls(), null, 2)).toBe(ControlResponseState.COMPLETED)
      child = await running
      expect(antigravityNativeToolResult(await child.heldRequest(), callId)).toContain(outputMarker)
    }
    finally {
      watch.cancel()
    }
  }, async () => {
    if (child)
      await child.finish()
    else
      await native.modelScript.releaseGateIfHeld(gate)
  })
})
