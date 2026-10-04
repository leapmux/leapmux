import type { AmpSeededThread, AmpThreadView } from '../helpers/ampSurface'
import { realpathSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import { expect } from '@playwright/test'
import { typeAHandleLabel } from '../../../src/components/shell/resumeSession'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { AMP_E2E_THREADS_PATH } from '../helpers/ampSurface'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, loginViaToken, menuOptionLabel, openMenu, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { createGitRepo, openNewAgentDialog, setWorkingDir, waitForWorker } from '../helpers/worktree'

/**
 * The picker opens an external native session. The next model request must include that session's earlier context.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 *
 * Amp stores threads on its service. The native thread list omits archived and empty threads. The mock seeds external threads. The picker filters them to the working directory. `amp threads continue` resumes the selected thread.
 */
ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

const SESSION_MENU = 'session-select-menu'

const NEW_SESSION_ROW = 'Start a new session'

// `false`: an Amp thread is an id, not a file path.
const TYPE_A_HANDLE_ROW = typeAHandleLabel(false)

async function seedThread(mockModelUrl: string, thread: AmpSeededThread): Promise<void> {
  const response = await fetch(`${mockModelUrl}${AMP_E2E_THREADS_PATH}`, { method: 'POST', body: JSON.stringify(thread) })
  expect(response.status).toBe(201)
}

function savedMessages(label: string): AmpSeededThread['messages'] {
  return [
    { role: 'user', text: 'Remember the earlier topic.' },
    { role: 'assistant', text: `${label} was the earlier answer.` },
    { role: 'user', text: 'Keep that answer available.' },
  ]
}

ampTest('offers the workspace\'s Amp threads and resumes the one picked', async ({ page, leapmuxServer, modelScript }) => {
  const { hubUrl, adminToken, workerId, dataDir, mockModelUrl } = leapmuxServer
  const subjectDir = createGitRepo(dataDir, `amp-picker-subject-${crypto.randomUUID()}`)
  const otherDir = createGitRepo(dataDir, `amp-picker-other-${crypto.randomUUID()}`)
  const subjectTree = pathToFileURL(realpathSync(subjectDir)).href
  const seeded = `T-${crypto.randomUUID()}`
  const priorAnswer = 'AMP_PRIOR_ANSWER_MARKER'
  await seedThread(mockModelUrl, { id: seeded, title: 'Seeded Amp thread', tree: subjectTree, messages: savedMessages(priorAnswer) })
  await seedThread(mockModelUrl, { id: `T-${crypto.randomUUID()}`, title: 'Archived Amp thread', tree: subjectTree, messages: savedMessages('Archived'), archived: true })
  await seedThread(mockModelUrl, { id: `T-${crypto.randomUUID()}`, title: 'Empty Amp thread', tree: subjectTree, messages: [] })
  await seedThread(mockModelUrl, { id: `T-${crypto.randomUUID()}`, title: 'Other workspace thread', tree: pathToFileURL(realpathSync(otherDir)).href, messages: savedMessages('Other workspace') })

  // An agent keeps a tab in the workspace, so the New Agent dialog stays reachable.
  const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `Amp Picker ${crypto.randomUUID()}`)
  await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, otherDir, {
    agentProvider: AgentProvider.AMP,
    ...agentOpenOptions(agentSettings(AgentProvider.AMP)),
    title: 'Keeper',
  })
  await loginViaToken(page, adminToken)
  await openWorkspace(page, workspaceId)

  await openNewAgentDialog(page)
  await waitForWorker(page)
  const dialog = page.getByRole('dialog')
  await dialog.getByTestId('agent-provider-selector-trigger').click()
  await page.getByTestId(`agent-provider-option-${AgentProvider.AMP}`).click()
  await expect(dialog.getByTestId('agent-provider-selector-trigger')).toContainText('Amp')
  await setWorkingDir(page, subjectDir)

  const trigger = dialog.getByTestId(`${SESSION_MENU}-trigger`)
  await expect(trigger).toBeEnabled()
  await openMenu(dialog, SESSION_MENU)
  // The one resumable thread of the workspace, under the two rows that are not
  // threads. The archived, the empty and the other workspace's threads are absent.
  const options = dialog.getByTestId(SESSION_MENU).getByRole('menuitemradio')
  await expect(options).toHaveCount(3)
  await expect(options.first()).toHaveText(NEW_SESSION_ROW)
  await expect(options.nth(1)).toHaveText(TYPE_A_HANDLE_ROW)
  const threadRow = options.nth(2)
  await expect(menuOptionLabel(threadRow)).toHaveText('Seeded Amp thread')
  await expect(threadRow).toHaveAttribute('data-testid', `loading-menu-option-${seeded}`)

  await threadRow.click()
  await expect(trigger).toHaveAttribute('data-value', seeded)
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByRole('heading', { name: 'New Agent' })).toBeHidden()

  // The resumed tab continues the seeded thread: its messages land in that thread.
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)
  await expectAssistantAnswer(page)
  const resumed = JSON.stringify(status.requests.find(request => request.stepIndex === 0)?.body)
  expect(resumed).toContain(priorAnswer)
  expect(resumed).toContain(ARITHMETIC_PROMPT)
  const threads = await (await fetch(`${mockModelUrl}${AMP_E2E_THREADS_PATH}`)).json() as AmpThreadView[]
  expect(threads.find(thread => thread.id === seeded)?.messageCount).toBe(5)
})
