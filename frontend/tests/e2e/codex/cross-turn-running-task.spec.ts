import type { Locator } from '@playwright/test'
import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelStep } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext, NativeContextFixtures } from '../helpers/nativeScenario'
import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { CODEX_ITEM } from '../../../src/generated/contracts/codex-protocol'
import { AgentActivityState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickString } from '../../../src/lib/jsonPick'
import { codexTest } from '../codex-fixtures'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { isAlive } from '../helpers/processTree'
import { codexExecToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import {
  assistantBubbles,
  interruptButton,
  openWorkspace,
  queuePauseButton,
  resumePausedQueue,
  sendMessage,
  toolCallRow,
  waitForAgentIdle,
} from '../helpers/ui'
import { closeNativeAgentAndWait } from '../helpers/workerTabs'
import { waitForCodexCommandStart } from './commandStart'
import { nativeContext } from './scenarios'

interface CrossTurnBackgroundCommandScenario {
  fixtures: NativeContextFixtures
  slug: string
  beforeText: string
  afterText: string
  prompt: string
  turnEndStep: Omit<MockModelStep, 'gate'>
  waitForTurnEndStep?: boolean
  beforeRelease?: (state: {
    context: ManagedNativeScenarioContext
    pending: Locator
  }) => Promise<void>
}

function findCommandSpanId(messages: readonly AgentChatMessage[], marker: string): string | undefined {
  for (const message of messages) {
    if (message.spanType !== CODEX_ITEM.CommandExecution || !message.spanId)
      continue
    const body = nativeMessageBody(message)
    if (isObject(body) && isObject(body.item) && typeof body.item.command === 'string' && body.item.command.includes(marker))
      return message.spanId
  }
  return undefined
}

function isCodexTurnCompleted(message: AgentChatMessage): boolean {
  const body = nativeMessageBody(message)
  if (!isObject(body))
    return false
  if (pickString(body, 'method') === 'turn/completed')
    return true
  return isObject(body.turn) && typeof body.turn.status === 'string'
}

/**
 * Start a background shell command controlled by flag files so it stays open across a turn divider.
 * Verify the open span rail and pending UI, run any extra checks before release,
 * then release the command and verify natural completion and cleanup.
 */
async function runCrossTurnBackgroundCommand(options: CrossTurnBackgroundCommandScenario): Promise<void> {
  const { fixtures, slug, beforeText, afterText, prompt, turnEndStep, waitForTurnEndStep = false, beforeRelease } = options
  const { page, modelScript } = fixtures
  const context = await nativeContext(fixtures)
  const { workingDir } = await openNativeAgent(context, { directoryPrefix: `codex-cross-turn-${slug}-` })
  const agent = await currentNativeAgent(context)
  const emitFlag = join(workingDir, `${slug}-emit.flag`)
  const releaseFlagName = `${slug}-release.flag`
  const releaseFlag = join(workingDir, releaseFlagName)
  const gate = `codex-cross-turn-${slug}-end`
  const command = [
    `while [ ! -e ${quotePosixShellArgument(emitFlag)} ]; do sleep 0.05; done`,
    `printf '%s\\n' ${quotePosixShellArgument(beforeText)}`,
    `while [ ! -e ${quotePosixShellArgument(releaseFlag)} ]; do sleep 0.05; done`,
    `printf '%s\\n' ${quotePosixShellArgument(afterText)}`,
  ].join('; ')

  try {
    const start = await modelScript.queue(
      {
        toolCalls: [
          codexExecToolCall(
            `exec-bg-${slug}`,
            `text(await tools.exec_command({cmd:${JSON.stringify(command)},yield_time_ms:300,max_output_tokens:500}));`,
          ),
        ],
      },
      { ...turnEndStep, gate },
    )

    await sendMessage(page, modelScript.prompt(prompt))
    await waitForCodexCommandStart(context)
    writeFileSync(emitFlag, '')

    const pending = page.locator('[data-testid="tool-pending"]:visible')
    await expect(pending).toContainText(beforeText)

    await modelScript.releaseGate(gate)
    if (waitForTurnEndStep)
      await modelScript.waitForSteps(start + 2)

    const divider = page.locator('[data-testid="result-divider"]:visible').first()
    await expect(divider).toBeVisible()

    let commandSpanId = ''
    await retryUntilPass(async () => {
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      const spanId = findCommandSpanId(snapshot.messages, releaseFlagName)
      expect(spanId, 'Worker stored the commandExecution start row').toBeTruthy()
      commandSpanId = spanId!
      const dividerMessage = snapshot.messages.find(isCodexTurnCompleted)
      expect(dividerMessage, 'Worker stored the turn divider').toBeDefined()
      expect(dividerMessage!.spanLines, 'span rail stays open behind the turn divider').toContain(commandSpanId)
      expect(snapshot.messages.filter(message => message.spanId === commandSpanId), 'only opening row exists before release').toHaveLength(1)
    })

    await expect(pending).toBeVisible()
    await expect(pending).toContainText(beforeText)
    await expect(interruptButton(page)).toBeVisible()

    if (beforeRelease)
      await beforeRelease({ context, pending })

    // Release the background command and verify one native completion closes the rail.
    writeFileSync(releaseFlag, '')

    const resultRow = toolCallRow(page, commandSpanId)
    await expect(resultRow).toHaveCount(1)
    await expect(resultRow).toHaveAttribute('data-tool-status', 'completed')
    await expect(resultRow).toContainText(afterText)
    await expect(pending).toHaveCount(0)

    await waitForAgentIdle(page)
    await expect(interruptButton(page)).toHaveCount(0)
    await retryUntilPass(async () => {
      expect((await currentNativeAgent(context)).activityState).toBe(AgentActivityState.IDLE)
      const snapshot = await readNativeMessageSnapshot(context, agent.id)
      expect(snapshot.messages.filter(message => message.spanId === commandSpanId), 'exactly one opening and one closing row').toHaveLength(2)
    })
  }
  finally {
    writeFileSync(emitFlag, '')
    writeFileSync(releaseFlag, '')
    await modelScript.releaseGateIfHeld(gate)
    await closeNativeAgentAndWait(context, agent.id)
  }
}

codexTest.describe('cross-turn running task', () => {
  codexTest('keeps a running background command open across a normal turn divider and page reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
    await runCrossTurnBackgroundCommand({
      fixtures: {
        page,
        modelScript,
        leapmuxServer,
        workspaceId: authenticatedEmptyWorkspace.workspaceId,
      },
      slug: 'normal',
      beforeText: 'BEFORE_TURN_END',
      afterText: 'AFTER_RELEASE',
      prompt: 'Start the background command and finish turn one.',
      turnEndStep: { text: 'Turn one finished while command runs.' },
      waitForTurnEndStep: true,
      beforeRelease: async ({ context, pending }) => {
        expect((await currentNativeAgent(context)).activityState).toBe(AgentActivityState.WORKING)

        // Reload while the release flag is still absent: rail, pending card, and live outputTail must survive reload.
        await page.reload()
        await openWorkspace(page, context.workspaceId)

        await expect(page.locator('[data-testid="result-divider"]:visible').first()).toBeVisible()
        await expect(pending).toBeVisible()
        await expect(pending).toContainText('BEFORE_TURN_END')
        await expect(interruptButton(page)).toBeVisible()

        const dividerRow = page.locator('[data-span-columns]:not([data-span-columns="0"]):visible').filter({
          has: page.locator('[data-testid="result-divider"]'),
        })
        await expect(dividerRow).toHaveCount(1)
      },
    })
  })

  codexTest('keeps a running background command open across an HTTP 400 error turn divider', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
    await runCrossTurnBackgroundCommand({
      fixtures: {
        page,
        modelScript,
        leapmuxServer,
        workspaceId: authenticatedEmptyWorkspace.workspaceId,
      },
      slug: 'error',
      beforeText: 'ERROR_TURN_BEFORE',
      afterText: 'ERROR_TURN_AFTER',
      prompt: 'Start the background command before a model HTTP 400 error.',
      turnEndStep: {
        error: { status: 400, message: 'Emulated HTTP 400 after background command started.' },
      },
    })
  })

  codexTest('closes an idle background command via the Interrupt escape hatch and accepts a follow-up turn', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({
      page,
      modelScript,
      leapmuxServer,
      workspaceId: authenticatedEmptyWorkspace.workspaceId,
    })
    const { workingDir } = await openNativeAgent(context, { directoryPrefix: 'codex-cross-turn-idle-int-' })
    const agent = await currentNativeAgent(context)
    const emitFlag = join(workingDir, 'idle-int-emit.flag')
    const pidFile = join(workingDir, 'idle-int.pid')
    const gate = 'codex-cross-turn-idle-int-end'
    const command = [
      `echo $$ > ${quotePosixShellArgument(pidFile)}`,
      `while [ ! -e ${quotePosixShellArgument(emitFlag)} ]; do sleep 0.05; done`,
      `printf 'IDLE_INT_RUNNING\\n'`,
      `exec sleep 60`,
    ].join('; ')

    try {
      const start = await modelScript.queue(
        {
          toolCalls: [
            codexExecToolCall(
              'exec-bg-idle-int',
              `text(await tools.exec_command({cmd:${JSON.stringify(command)},yield_time_ms:300,max_output_tokens:500}));`,
            ),
          ],
        },
        { gate, text: 'Turn one finished before idle interrupt.' },
      )

      await sendMessage(page, modelScript.prompt('Start a long background sleep command and finish turn one.'))
      await waitForCodexCommandStart(context)
      writeFileSync(emitFlag, '')

      const pending = page.locator('[data-testid="tool-pending"]:visible')
      await expect(pending).toContainText('IDLE_INT_RUNNING')

      await modelScript.releaseGate(gate)
      await modelScript.waitForSteps(start + 2)
      await expect(page.locator('[data-testid="result-divider"]:visible').first()).toBeVisible()

      let commandSpanId = ''
      await retryUntilPass(async () => {
        const snapshot = await readNativeMessageSnapshot(context, agent.id)
        const spanId = findCommandSpanId(snapshot.messages, 'idle-int.pid')
        expect(spanId).toBeTruthy()
        commandSpanId = spanId!
      })
      const commandPid = Number.parseInt(readFileSync(pidFile, 'utf8').trim(), 10)
      expect(Number.isInteger(commandPid) && commandPid > 0).toBe(true)

      const interruptBtn = interruptButton(page)
      await expect(interruptBtn).toBeVisible()
      expect((await currentNativeAgent(context)).activityState).toBe(AgentActivityState.WORKING)

      // Click Interrupt while the turn is idle and the background command is still open.
      await interruptBtn.click()

      await expect(pending).toHaveCount(0)
      const resultRow = toolCallRow(page, commandSpanId)
      await expect(resultRow).toHaveCount(1)
      await expect(resultRow).toHaveAttribute('data-tool-status', 'cancelled')

      await waitForAgentIdle(page)
      await expect(interruptBtn).toHaveCount(0)
      await retryUntilPass(async () => {
        expect(isAlive(commandPid), 'background sleep process must terminate on idle interrupt').toBe(false)
        expect((await currentNativeAgent(context)).activityState).toBe(AgentActivityState.IDLE)
        const snapshot = await readNativeMessageSnapshot(context, agent.id)
        expect(snapshot.messages.filter(message => message.spanId === commandSpanId), 'exactly one opening row and one interrupted closing row').toHaveLength(2)
      })

      // Follow-up Turn 2 works cleanly after the idle interrupt escape hatch.
      if (await queuePauseButton(page).textContent() === 'Resume Queue')
        await resumePausedQueue(page)
      const followUpStart = await modelScript.queue({ text: 'Turn two answered cleanly after idle interrupt.' })
      await sendMessage(page, modelScript.prompt('Confirm turn two runs cleanly.'))
      await modelScript.waitForSteps(followUpStart + 1)
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: 'Turn two answered cleanly after idle interrupt.' }).first()).toBeVisible()
    }
    finally {
      writeFileSync(emitFlag, '')
      await modelScript.releaseGateIfHeld(gate)
      await closeNativeAgentAndWait(context, agent.id)
    }
  })
})
