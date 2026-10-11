import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { clineTest } from '../cline-fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { createFromSessionRow, expectNativeResumeContext, expectReopenedNativeAgent, expectResumedAnswerUnmerged, nativeResumeTexts, openNewAgentFor, openSoleSessionRow } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { hubSpawnEnv } from '../helpers/server'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, loginViaToken, menuOptionLabel, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { CLINE_AGENT } from './scenarios'

/** The longest the one-shot CLI run may take on a loaded machine. */
const CLI_RUN_TIMEOUT_MS = 120_000

/**
 * Run one prompt through the `cline` CLI in `cwd`, as a user would in a terminal.
 * The local session backend keeps the run off every hub.
 */
async function runClineOnce(cwd: string, agentEnv: Record<string, string>, prompt: string): Promise<void> {
  await promisify(execFile)('cline', ['--json', prompt], {
    cwd,
    env: { ...hubSpawnEnv(), ...agentEnv, CLINE_SESSION_BACKEND_MODE: 'local' },
    timeout: CLI_RUN_TIMEOUT_MS,
    maxBuffer: 16 << 20,
  })
}

/**
 * The picker opens an external native session. The next model request must include that session's earlier context.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * The test creates the external session with Cline's installed CLI and isolated environment. `cline history --json` lists it. The Worker recreates the selected session with its stored messages.
 */
clineTest('offers a Cline session of the working directory and resumes the one picked', async ({ page, leapmuxServer, modelScript }) => {
  const { hubUrl, adminToken, workerId, agentEnv } = leapmuxServer
  // The picker keeps the sessions of one working directory, so two directories of the rule of Cline keep them apart.
  const subjectDir = newProviderWorkingDir(CLINE_AGENT, 'cline-picker-subject-')
  const otherDir = newProviderWorkingDir(CLINE_AGENT, 'cline-picker-other-')

  // One session in the subject directory, and one in another directory. The
  // session title is the first line of the prompt, so the marker goes on the
  // second line.
  const texts = nativeResumeTexts()
  const seededStep = await modelScript.queue({ text: `The seeded answer. ${texts.originalAnswer}` }, { text: 'The other answer.' })
  await runClineOnce(subjectDir, agentEnv, modelScript.prompt(`Seeded Cline session\n${texts.originalPrompt}`))
  await runClineOnce(otherDir, agentEnv, modelScript.prompt('Other directory session'))
  await modelScript.waitForSteps(seededStep + 2)

  // An agent keeps a tab in the workspace, so the New Agent dialog stays reachable.
  const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `Cline Picker ${crypto.randomUUID()}`)
  const keeperId = await openAgentViaAPI({ hubUrl, adminToken, workerId }, workspaceId, otherDir, {
    ...agentOpenOptions(AgentProvider.CLINE),
    title: 'Keeper',
  })
  await loginViaToken(page, adminToken)
  await openWorkspace(page, workspaceId)

  const dialog = await openNewAgentFor(page, AgentProvider.CLINE, subjectDir)
  await expect(dialog.getByTestId('agent-provider-selector-trigger')).toContainText('Cline')
  // The one session of the subject directory, under the two rows that are not
  // sessions. The other directory's session is absent.
  const sessionRow = await openSoleSessionRow(dialog)
  // The title is the first line of the stored prompt, without the marker line.
  await expect(menuOptionLabel(sessionRow)).toHaveText('Seeded Cline session')
  const sessionId = (await sessionRow.getAttribute('data-testid') ?? '').replace(/^loading-menu-option-/, '')
  expect(sessionId).not.toBe('')
  await createFromSessionRow(dialog, sessionRow, sessionId)
  const reopened = await expectReopenedNativeAgent({ page, leapmuxServer }, { agentProvider: AgentProvider.CLINE, agentSessionId: sessionId }, [keeperId])

  // The resumed tab continues the seeded session: its model call carries the
  // stored conversation.
  const resumedStep = await modelScript.queue({ text: `${ARITHMETIC_ANSWER_TEXT} ${texts.resumedAnswer}` })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps(resumedStep + 1)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  const resumedRequest = await modelScript.requestAt(resumedStep)
  const resumed = JSON.stringify(resumedRequest.body)
  expect(resumed).toContain('Seeded Cline session')
  expect(resumed).toContain('The seeded answer.')
  expect(resumed).not.toContain('The other answer.')
  expectNativeResumeContext(nativeModelConversationTurns(resumedRequest), { ...texts, resumedPrompt: ARITHMETIC_PROMPT })
  // An external session opens without Worker rows to copy, so only the separate resumed answer is provable here.
  await expectResumedAnswerUnmerged({ page, leapmuxServer }, reopened.id, texts)
})
