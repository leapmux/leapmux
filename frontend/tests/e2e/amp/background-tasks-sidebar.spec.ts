import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cleanName } from '../../../src/lib/validate'
import { ampTest } from '../amp-fixtures'
import { finishCleanup } from '../helpers/cleanup'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { isAlive } from '../helpers/processTree'
import { backgroundBashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, requireRegistryRow } from '../helpers/subagentRegistry'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { assistantBubbles, interruptButton, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openOpaqueAmpTask } from './opaqueTask'

/**
 * Amp exposes a remote Task as a registry row without a child transcript.
 * A native background shell command remains active after its first turn ends.
 * An interrupt stops the Amp process and its owned background command.
 */
/** The native child report also appears in the parent's tool result. */
const REPORT = 'Apple, banana, cherry. One, two, three. Done.'

ampTest.describe('Amp subagent registry', () => {
  ampTest('follows a subagent from its call to its report', async ({ native }) => {
    const { page, modelScript, leapmuxServer } = native
    await expectNoRegistryRows(page, leapmuxServer)
    const child = await openOpaqueAmpTask(native, {
      report: REPORT,
      ruleName: 'the subagent reports',
      callId: 'spawn-amp',
      task: { description: 'Run the fruit task', prompt: 'List three fruits, then count to three, then report done.' },
      parentPrompt: 'Spawn one subagent for the counting task and report what it says.',
      parentAnswer: 'The subagent listed three fruits and counted to three.',
    })
    try {
      await expect(child.row).toContainText('Run the fruit task')
      await child.finish()
      await expectRowBecomesFinal(page, child.row)
      await expect(child.row).toHaveAttribute('data-status', 'succeeded')
      await expectSectionPersists(page)
      expect((await modelScript.status()).ruleMatches['the subagent reports']).toBe(1)
      expect(await child.row.getAttribute('data-child-agent-id') ?? '').toBe('')
      await expect.poll(async () => (await messageContents(page).allTextContents()).join(' ')).toContain(REPORT)
    }
    finally {
      await child.finish()
    }
  })

  ampTest('follows a background command until the Amp process exits', async ({ native }) => {
    const { page, modelScript, leapmuxServer } = native
    await expectNoRegistryRows(page, leapmuxServer)
    const agent = await currentNativeAgent(native)
    const control = createToolOutputControl(agent.workingDir)
    const pidFileName = 'amp-background-command.pid'
    const pidFile = join(agent.workingDir, pidFileName)
    // The registry titles a row with its command cut to NAME_BYTE_LIMIT bytes (`cleanName`, and
    // `bgtask.Upsert.Clean` in the Worker). The node path and the working directory differ on each
    // machine, so an assignment in front of them keeps the file name inside the title.
    // Amp runs the command in the turn's working directory, so the relative name resolves to pidFile.
    const program = `require("node:fs").writeFileSync(process.env.AMP_BACKGROUND_PID_FILE,String(process.pid));require(${JSON.stringify(control.scriptPath)})`
    const command = `AMP_BACKGROUND_PID_FILE=${pidFileName} ${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(program)}`
    const gate = 'amp-background-interrupt-response'
    try {
      await modelScript.queue(
        { toolCalls: [backgroundBashToolCall(AgentProvider.AMP, 'bg-shell', command)] },
        { text: 'The command runs in the background.' },
      )
      await sendMessage(page, modelScript.prompt('Start the long command in the background.'))
      await control.waitForFirstOutput()
      const row = await requireRegistryRow(page, 'shell')
      await expect(row).toContainText(pidFileName)
      await expect(row).toContainText(cleanName(command))
      await modelScript.waitForSteps()
      await expect(assistantBubbles(page).filter({ hasText: 'The command runs in the background.' })).not.toHaveCount(0)
      await expect(row).toHaveAttribute('data-status', 'running')
      expect(existsSync(pidFile)).toBe(true)
      const commandPid = Number(readFileSync(pidFile, 'utf8'))
      expect(Number.isSafeInteger(commandPid) && commandPid > 0).toBe(true)
      expect(isAlive(commandPid)).toBe(true)

      await modelScript.queue({ text: 'An essay.', gate })
      modelScript.allowUnconsumed('The native Amp interruption cancels this held answer.')
      await sendMessage(page, modelScript.prompt('Write a long essay about the history of computing.'))
      await modelScript.waitForGate(gate)
      await interruptButton(page).click()
      await waitForAgentIdle(page)
      await expectRowBecomesFinal(page, row)
      await expect(row).toHaveAttribute('data-status', 'stopped')
      await expect.poll(() => isAlive(commandPid)).toBe(false)
    }
    finally {
      await finishCleanup([control.releaseFirstOutput(), control.releaseFinalOutput(), modelScript.releaseGateIfHeld(gate)])
    }
  })
})
