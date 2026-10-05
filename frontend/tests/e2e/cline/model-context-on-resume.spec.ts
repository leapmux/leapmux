import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { expect } from '@playwright/test'
import { typeAHandleLabel } from '../../../src/components/shell/resumeSession'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { expectNativeResumeContext, expectReopenedNativeAgent, expectResumedAnswerUnmerged, nativeResumeTexts } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { hubSpawnEnv } from '../helpers/server'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, loginViaToken, menuOptionLabel, openMenu, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { createGitRepo, openNewAgentDialog, setWorkingDir, waitForWorker } from '../helpers/worktree'

/**
 * The picker opens an external native session. The next model request must include that session's earlier context.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * The test creates the external session with Cline's installed CLI and isolated environment. `cline history --json` lists it. The Worker recreates the selected session with its stored messages.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

const SESSION_MENU = 'session-select-menu'

const NEW_SESSION_ROW = 'Start a new session'

// `false`: a Cline session is an id, not a file path.
const TYPE_A_HANDLE_ROW = typeAHandleLabel(false)

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

clineTest('offers a Cline session of the working directory and resumes the one picked', async ({ page, leapmuxServer, modelScript }) => {
  const { hubUrl, adminToken, workerId, dataDir, agentEnv } = leapmuxServer
  const subjectDir = createGitRepo(dataDir, `cline-picker-subject-${crypto.randomUUID()}`)
  const otherDir = createGitRepo(dataDir, `cline-picker-other-${crypto.randomUUID()}`)

  // One session in the subject directory, and one in another directory. The
  // session title is the first line of the prompt, so the marker goes on the
  // second line.
  const texts = nativeResumeTexts()
  await modelScript.queue({ text: `The seeded answer. ${texts.originalAnswer}` }, { text: 'The other answer.' })
  await runClineOnce(subjectDir, agentEnv, modelScript.prompt(`Seeded Cline session\n${texts.originalPrompt}`))
  await runClineOnce(otherDir, agentEnv, modelScript.prompt('Other directory session'))
  await modelScript.waitForSteps()

  // An agent keeps a tab in the workspace, so the New Agent dialog stays reachable.
  const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `Cline Picker ${crypto.randomUUID()}`)
  const keeperId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, otherDir, {
    agentProvider: AgentProvider.CLINE,
    ...agentOpenOptions(agentSettings(AgentProvider.CLINE)),
    title: 'Keeper',
  })
  await loginViaToken(page, adminToken)
  await openWorkspace(page, workspaceId)

  await openNewAgentDialog(page)
  await waitForWorker(page)
  const dialog = page.getByRole('dialog')
  await dialog.getByTestId('agent-provider-selector-trigger').click()
  await page.getByTestId(`agent-provider-option-${AgentProvider.CLINE}`).click()
  await expect(dialog.getByTestId('agent-provider-selector-trigger')).toContainText('Cline')
  await setWorkingDir(page, subjectDir)

  const trigger = dialog.getByTestId(`${SESSION_MENU}-trigger`)
  await expect(trigger).toBeEnabled()
  await openMenu(dialog, SESSION_MENU)
  // The one session of the subject directory, under the two rows that are not
  // sessions. The other directory's session is absent.
  const options = dialog.getByTestId(SESSION_MENU).getByRole('menuitemradio')
  await expect(options).toHaveCount(3)
  await expect(options.first()).toHaveText(NEW_SESSION_ROW)
  await expect(options.nth(1)).toHaveText(TYPE_A_HANDLE_ROW)
  const sessionRow = options.nth(2)
  // The title is the first line of the stored prompt, without the marker line.
  await expect(menuOptionLabel(sessionRow)).toHaveText('Seeded Cline session')
  const sessionId = (await sessionRow.getAttribute('data-testid') ?? '').replace(/^loading-menu-option-/, '')
  expect(sessionId).not.toBe('')

  await sessionRow.click()
  await expect(trigger).toHaveAttribute('data-value', sessionId)
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByRole('heading', { name: 'New Agent' })).toBeHidden()
  const reopened = await expectReopenedNativeAgent({ page, leapmuxServer }, { agentProvider: AgentProvider.CLINE, agentSessionId: sessionId }, [keeperId])

  // The resumed tab continues the seeded session: its model call carries the
  // stored conversation.
  await modelScript.queue({ text: `${ARITHMETIC_ANSWER_TEXT} ${texts.resumedAnswer}` })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  // The two CLI runs consumed steps 0 and 1.
  const resumedRequest = status.requests.find(request => request.stepIndex === 2)
  if (!resumedRequest)
    throw new Error('The resumed prompt reached no native model request.')
  const resumed = JSON.stringify(resumedRequest.body)
  expect(resumed).toContain('Seeded Cline session')
  expect(resumed).toContain('The seeded answer.')
  expect(resumed).not.toContain('The other answer.')
  expectNativeResumeContext(nativeModelConversationTurns(resumedRequest), { ...texts, resumedPrompt: ARITHMETIC_PROMPT })
  // An external session opens without Worker rows to copy, so only the separate resumed answer is provable here.
  await expectResumedAnswerUnmerged({ page, leapmuxServer }, reopened.id, texts)
})
