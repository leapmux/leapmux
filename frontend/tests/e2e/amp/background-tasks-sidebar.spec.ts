import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { finishCleanup } from '../helpers/cleanup'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { isAlive } from '../helpers/processTree'
import { backgroundBashToolCall } from '../helpers/providerToolCalls'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, requireRegistryRow } from '../helpers/subagentRegistry'
import { createToolOutputControl } from '../helpers/toolOutputControl'
import { assistantBubbles, messageContents, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openOpaqueAmpTask } from './opaqueTask'

/**
 * Amp exposes a remote Task as a registry row without a child transcript.
 * A native background shell command remains active after its first turn ends.
 * An interrupt stops the Amp process and its owned background command.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

/** The native child report also appears in the parent's tool result. */
const REPORT = 'Apple, banana, cherry. One, two, three. Done.'

ampTest.describe('Amp subagent registry', () => {
  ampTest('follows a subagent from its call to its report', async ({ authenticatedAmpWorkspace, page, modelScript, leapmuxServer }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
    await expectNoRegistryRows(page, leapmuxServer)
    const child = await openOpaqueAmpTask(context, {
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
      await expect(child.row).toHaveAttribute('data-status', 'completed')
      await expectSectionPersists(page)
      expect((await modelScript.status()).ruleMatches['the subagent reports']).toBe(1)
      expect(await child.row.getAttribute('data-child-agent-id') ?? '').toBe('')
      await expect.poll(async () => (await messageContents(page).allTextContents()).join(' ')).toContain(REPORT)
    }
    finally {
      await child.finish()
    }
  })

  ampTest('follows a background command until the Amp process exits', async ({ authenticatedAmpWorkspace, page, modelScript, leapmuxServer }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
    await expectNoRegistryRows(page, leapmuxServer)
    const agent = await currentNativeAgent(context)
    const control = createToolOutputControl(agent.workingDir)
    const pidFile = join(agent.workingDir, 'amp-background-command.pid')
    const program = `require('node:fs').writeFileSync(${JSON.stringify(pidFile)},String(process.pid));require(${JSON.stringify(control.scriptPath)})`
    const command = `${quotePosixShellArgument(process.execPath)} -e ${quotePosixShellArgument(program)}`
    const gate = 'amp-background-interrupt-response'
    try {
      await modelScript.queue(
        { toolCalls: [backgroundBashToolCall(AgentProvider.AMP, 'bg-shell', command)] },
        { text: 'The command runs in the background.' },
      )
      await sendMessage(page, modelScript.prompt('Start the long command in the background.'))
      await control.waitForFirstOutput()
      const row = await requireRegistryRow(page, 'shell')
      await expect(row).toContainText('amp-background-command.pid')
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
      await page.locator('[data-testid="interrupt-button"]:visible').click()
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
