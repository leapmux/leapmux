import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseSessionReset, heldToolScript } from '../helpers/nativeLifecycle'
import { resolveNativeProcessOwnership } from '../helpers/nativeProcessOwnership'
import { currentNativeAgent, nativeAgentById, nativeTextStep } from '../helpers/nativeScenario'
import { approveNativeToolsUntil } from '../helpers/nativeToolExecution'
import { isAlive, listProcesses } from '../helpers/processTree'
import { backgroundBashToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { getGlobalState } from '../helpers/server'
import { quotePosixShellArgument, uniqueMarker } from '../helpers/shellArguments'
import { applyPermissionPreset, assistantBubbles, expectSettingsChip, sendMessage, userBubbles, visibleOnly, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest('clears the native context while the saved transcript stays visible', async ({ native }) => {
  await exerciseSessionReset(native)
})

kimiTest('replaces the private runtime and stops its background command while retaining history', async ({ native }) => {
  const { page, modelScript } = native
  await waitForSettingsHydrated(page)
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsChip(page, 'Never Ask')
  const prior = await currentNativeAgent(native)
  if (!prior.workingDir)
    throw new Error('The native reset scenario requires a working directory.')
  const marker = uniqueMarker()
  const pidFile = join(prior.workingDir, `native-reset-${marker}.pid`)
  const releaseFile = `${pidFile}.release`
  const script = heldToolScript({ startedFile: pidFile, releaseFile })
  const prompt = `Start the background reset probe ${marker}.`
  const answer = `The background reset probe ${marker} started.`
  const start = await modelScript.queue(
    {
      toolCalls: [
        backgroundBashToolCall(AgentProvider.KIMI_CODE, 'held-reset-tool', `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(script)}`),
      ],
    },
    { text: answer },
  )
  await modelScript.fallback(nativeTextStep(native, 'The old runtime cancellation completed.'))
  let toolPid = 0
  try {
    await sendMessage(page, modelScript.prompt(prompt))
    await modelScript.waitForSteps(start + 2)
    await approveNativeToolsUntil(page, async () => existsSync(pidFile))
    toolPid = Number(readFileSync(pidFile, 'utf8'))
    expect(Number.isSafeInteger(toolPid) && toolPid > 0).toBe(true)
    const ownership = resolveNativeProcessOwnership(listProcesses(), toolPid, getGlobalState().binaryPath)
    expect(ownership.ownedPids).toContain(toolPid)
    expect(isAlive(ownership.rootPid)).toBe(true)
    const before = await currentNativeAgent(native)
    expect(before.agentSessionId).not.toBe('')
    await sendMessage(page, '/clear')
    await expect(visibleOnly(page.getByText('Context cleared', { exact: true })).first()).toBeVisible()
    await expect.poll(() => ownership.ownedPids.filter(isAlive), { message: 'reset stops the old private runtime and its held command' }).toEqual([])
    expect(isAlive(ownership.workerPid)).toBe(true)
    await retryUntilPass(async () => {
      const after = await nativeAgentById(native, before.id)
      expect(after?.agentSessionId).toEqual(expect.stringMatching(/\S/))
      expect(after?.agentSessionId).not.toBe(before.agentSessionId)
    })
    await sendNativeAnswer(native, `Continue after the runtime reset ${marker}.`, `The fresh runtime ${marker} answered.`)
    await expectSettingsChip(page, 'Never Ask')
    await expect(userBubbles(page).filter({ hasText: prompt }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: answer }).first()).toBeVisible()
  }
  finally {
    writeFileSync(releaseFile, 'release')
    if (toolPid > 0)
      await expect.poll(() => isAlive(toolPid), { message: 'the released probe exits before fixture cleanup' }).toBe(false)
  }
})
