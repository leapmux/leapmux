import type { McpProbeServer } from '../helpers/mcpProbeServer'
import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { writeMcpPermissionServer } from '../helpers/mcpPermissionServer'
import { expectTurnEndedAfter } from '../helpers/modelScriptFixture'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { assertPrivateNativePath } from '../helpers/nativeCredentialIsolation'
import { declinedToolCallId, exerciseNativePermissionDecision, exerciseNativePermissionReason, exerciseRememberedAllow, expectDeclinedToolRowAcrossReload } from '../helpers/nativePermission'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { bashToolCall, codexEscalatedCommandToolCall, mcpToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { getGlobalState } from '../helpers/server'
import { quotePosixShellArgument } from '../helpers/shellArguments'
import { answerControl, chooseSettingsOption, controlActions, controlButton, expectNoControlBanner, expectSettingsOptionChosen, isMaybeVisible, openWorkspace, savedControlAnswer, sendMessage, toolCallRow, toolRows, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { readCodexExecResult } from './execResult'
import { CODEX_AGENT, nativeContext } from './scenarios'

function writeCommand(path: string, content: string): string {
  return `printf %s ${quotePosixShellArgument(content)} > ${quotePosixShellArgument(path)}`
}

codexTest.describe('codex permission requests', () => {
  codexTest('runs a safe command without an approval request', async ({ native }) => {
    const { page } = native
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    // The observation fails if a banner shows at any time in the turn.
    let resultRequest: MockModelRequestRecord | undefined
    await expectNoNativeControl(native, {
      testId: 'control-banner',
      relatedProof: async () => {
        ({ resultRequest } = await runNativeToolTurn(native, {
          toolCalls: [bashToolCall(native.provider, 'safe-command', 'printf %s codex-safe-42')],
          prompt: 'Run the safe command once.',
          answer: 'The safe command finished.',
        }))
      },
    })

    // The command text holds the marker, so only the output field of the native result proves the run.
    if (!resultRequest)
      throw new Error('The safe Codex command turn returned no result request.')
    const result = readCodexExecResult(resultRequest, 'safe-command')
    expect(result.text).toContain('codex-safe-42')
    expect(result.failed).not.toBe(true)
    await expect(toolRows(page).filter({ hasText: 'codex-safe-42' }).first()).toBeVisible()
    await expectNoControlBanner(page)
  })

  codexTest('runs an escalated command only after approval', async ({ native, authenticatedCodexWorkspace }) => {
    const { page } = native
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir).toBeTruthy()
    const file = join(workingDir!, 'approved-command.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    await exerciseNativePermissionDecision(native, {
      toolCall: codexEscalatedCommandToolCall('approve-command', writeCommand(file, 'approved-42')),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('Run the scripted approval test.')
        expect(existsSync(file)).toBe(false)
      },
      nativeProof: () => {
        expect(readFileSync(file, 'utf8')).toBe('approved-42')
      },
      viewProof: async () => {
        await expectNoControlBanner(page)
        await expect(savedControlAnswer(page)).toHaveText('Allow')
      },
    })
  })

  codexTest('leaves the file absent after a denied escalated command', async ({ native, authenticatedCodexWorkspace }) => {
    const { page, modelScript } = native
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir).toBeTruthy()
    const file = join(workingDir!, 'denied-command.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    const start = await modelScript.queue(
      { toolCalls: [codexEscalatedCommandToolCall('deny-command', writeCommand(file, 'denied-42'))] },
    )
    await sendMessage(page, modelScript.prompt('Request approval for the scripted command.'))
    await modelScript.waitForSteps(start + 1)

    await waitForControlBanner(page)
    expect(existsSync(file)).toBe(false)
    // A command approval offers no decline: Codex states `cancel` as its refusal, so the Deny slot reads Cancel.
    await expect(controlButton(page, 'deny')).toHaveText('Cancel')
    await answerControl(page, 'deny')
    // Codex ends this code-mode cell on denial. It sends no second model request.
    await waitForAgentIdle(page)

    expect(existsSync(file)).toBe(false)
    await expectNoControlBanner(page)
    await expectTurnEndedAfter(modelScript, start + 1)
    await expect(savedControlAnswer(page)).toHaveText('Cancel')
    // Codex gives the command item an ID of its own, and its declined result row repeats no text of the command. So
    // the row is found by its state, and the request row of the same item names the file of the refused command.
    const callId = await declinedToolCallId(page)
    await expect(toolCallRow(page, callId, 'request')).toContainText('denied-command.txt')
    await expectDeclinedToolRowAcrossReload(native, callId)
  })

  // The reply to a command approval carries no reason. The reason follows as the reader's next message, which opens a
  // turn of its own, because Codex ends the refused turn.
  codexTest('sends the reader\'s typed refusal reason as the next message', async ({ native, authenticatedCodexWorkspace }) => {
    const { page } = native
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir).toBeTruthy()
    const file = join(workingDir!, 'reason-command.txt')
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')
    await exerciseNativePermissionReason(native, {
      toolCall: codexEscalatedCommandToolCall('reason-command', writeCommand(file, 'reason-42')),
      route: 'next-message',
      afterRefusal: 'ends',
      expectNotRun: () => expect(existsSync(file)).toBe(false),
      viewProof: () => expect(savedControlAnswer(page)).toHaveText('Cancel'),
    })
  })

  // Codex proposes a command rule for an escalated command. The Command rule pill sends that amendment, and Codex
  // writes it to the rule file of its home, so the same command runs in the next turn with no approval.
  codexTest('a command rule covers the same escalated command in the next turn', async ({ native, authenticatedCodexWorkspace, leapmuxServer }) => {
    const { page } = native
    const codexHome = leapmuxServer.agentEnv?.CODEX_HOME
    if (!codexHome)
      throw new Error('The command rule scenario requires the isolated Codex home.')
    assertPrivateNativePath(codexHome, getGlobalState().tmpDir)
    const workingDir = authenticatedCodexWorkspace.workingDir
    expect(workingDir).toBeTruthy()
    const file = join(workingDir!, 'rule-command.txt')
    // Each run appends the marker, so the file states how many runs happened.
    const command = `printf %s rule-42 >> ${quotePosixShellArgument(file)}`
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')
    await exerciseRememberedAllow(native, {
      scopeGroup: 'Allow as',
      scope: 'Command rule',
      firstCall: codexEscalatedCommandToolCall('rule-first', command),
      secondCall: codexEscalatedCommandToolCall('rule-second', command),
      beforeDecision: () => expect(existsSync(file)).toBe(false),
      firstProof: () => expect(readFileSync(file, 'utf8')).toBe('rule-42'),
      secondProof: () => expect(readFileSync(file, 'utf8')).toBe('rule-42rule-42'),
      ruleFiles: [join(codexHome, 'rules', 'default.rules')],
    })
  })

  // Codex asks before an MCP tool through an MCP elicitation that states `codex_approval_kind`, which is a path of its
  // own beside the command approval. The server of the configuration asks for each of its tools.
  codexTest('denies an MCP tool before the server receives it', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const codexHome = leapmuxServer.agentEnv?.CODEX_HOME
    if (!codexHome)
      throw new Error('The MCP permission scenario requires the isolated Codex home.')
    assertPrivateNativePath(codexHome, getGlobalState().tmpDir)
    const directory = newProviderWorkingDir(CODEX_AGENT, 'codex-mcp-permission-')
    const server = writeMcpPermissionServer(directory)
    await withAskingMcpServer(codexHome, server, async () => {
      const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
      await openProviderAgent(leapmuxServer, context.workspaceId, CODEX_AGENT, { workingDir: directory })
      await openWorkspace(page, context.workspaceId)
      await exerciseNativePermissionDecision(context, {
        toolCall: mcpToolCall(context.provider, 'codex-mcp-deny', { server: server.name, tool: 'touch', input: {} }),
        decision: 'deny',
        beforeDecision: async (banner) => {
          await expect(banner).toContainText('touch')
          // Codex listed the tools of the server, so the server runs and only the refusal keeps the call from it.
          expect(existsSync(server.ready)).toBe(true)
        },
        nativeProof: () => expect(existsSync(server.called)).toBe(false),
        viewProof: () => expectNoControlBanner(page),
      })
    })
  })
})

/**
 * Add `server` to the Codex configuration of the run, with each of its tools set to ask before it runs, and restore
 * the configuration afterwards. An agent reads the configuration when it starts, so `run` opens its own agent.
 */
async function withAskingMcpServer(codexHome: string, server: McpProbeServer, run: () => Promise<void>): Promise<void> {
  const path = join(codexHome, 'config.toml')
  const original = readFileSync(path, 'utf8')
  const section = [
    '',
    `[mcp_servers.${server.name}]`,
    `command = ${JSON.stringify(server.command)}`,
    `args = [${server.args.map(argument => JSON.stringify(argument)).join(', ')}]`,
    'default_tools_approval_mode = "prompt"',
    '',
  ].join('\n')
  await withCleanup(async () => {
    writeFileSync(path, `${original}${section}`, { mode: 0o600 })
    await run()
  }, async () => writeFileSync(path, original, { mode: 0o600 }))
}

/**
 * The command that the approval test scripts. It removes a directory that does not exist, so a run changes nothing,
 * and then prints a number that only the shell computes. The command text holds `$((40 + 2))`, not
 * {@link APPROVAL_OUTPUT}, so only a run of the command can put that output in its result.
 */
const APPROVAL_COMMAND = `rm -${'rf'} /tmp/codex-approval-test-dir-nonexistent && echo "codex-approved-$((40 + 2))"`

/** The output of {@link APPROVAL_COMMAND}. */
const APPROVAL_OUTPUT = 'codex-approved-42'

codexTest.describe('codex approval UI', () => {
  codexTest('approval flow works with on-request policy', async ({ native }) => {
    const { page, modelScript } = native

    // Switch to on-request approval policy so approval prompts appear.
    // The check of the chosen option closes the menus.
    await chooseSettingsOption(page, 'permissionMode-on-request')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'permissionMode-on-request')

    // `rm` always requires approval in on-request mode, so the command raises an approval request.
    // The answer step reads the result of the approved command; the fallback answers any request that Codex adds.
    await modelScript.fallback({ text: 'The command finished.' })
    const start = await modelScript.queue(
      { toolCalls: [bashToolCall(native.provider, 'approval-call', APPROVAL_COMMAND)] },
      { text: 'The approved command finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run this exact command.'))
    await modelScript.waitForSteps(start + 1)

    await waitForControlBanner(page)

    // The allow-choice pills show the allow decisions of Codex. The group appears only when the CLI offers `accept`
    // and a second allow decision, and the CLI chooses that second decision. So the test checks the pills only when
    // the group renders. This spec exists for the approval round trip below, so a missing radio must not fail it.
    // The pills sit in the control actions of the composer, not in the banner.
    const allowChoices = controlActions(page).getByRole('radiogroup', { name: 'Allow as' })
    if (await isMaybeVisible(allowChoices)) {
      const once = allowChoices.getByRole('radio', { name: 'Once' })
      await expect(once).toBeChecked()
      const remembering = allowChoices.getByRole('radio').nth(1)
      await remembering.click()
      await expect(remembering).toBeChecked()
      // Return to the one-turn decision before approval. A kept command rule would stay
      // in the Codex home of the run and answer the requests of later specs.
      await once.click()
      await expect(once).toBeChecked()
    }

    await answerControl(page, 'allow')

    // The approved command ran: the next model request holds its computed output as the result of the call.
    await modelScript.waitForSteps(start + 2)
    const result = readCodexExecResult(await modelScript.requestAt(start + 1), 'approval-call')
    expect(result.text, 'the result of the approved command holds the output that only its run computes').toContain(APPROVAL_OUTPUT)
    expect(result.failed).not.toBe(true)

    // Wait for the agent to finish. The chat then shows the output of the approved command.
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    await expect(toolRows(page).filter({ hasText: APPROVAL_OUTPUT }).first()).toBeVisible()
  })
})
