import type { AmpSeededThread, AmpThreadView } from '../helpers/ampSurface'
import { realpathSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { ampTest } from '../amp-fixtures'
import { AMP_E2E_THREADS_PATH } from '../helpers/ampSurface'
import { createWorkspaceViaAPI, openAgentViaAPI } from '../helpers/api'
import { createFromSessionRow, expectNativeResumeContext, expectReopenedNativeAgent, expectResumedAnswerUnmerged, nativeResumeTexts, openNewAgentFor, openSoleSessionRow } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, loginViaToken, menuOptionLabel, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { createGitRepo } from '../helpers/worktree'

async function seedThread(mockModelUrl: string, thread: AmpSeededThread): Promise<void> {
  const response = await fetch(`${mockModelUrl}${AMP_E2E_THREADS_PATH}`, { method: 'POST', body: JSON.stringify(thread) })
  expect(response.status).toBe(201)
}

function savedMessages(label: string, prompt = 'Remember the earlier topic.'): AmpSeededThread['messages'] {
  return [
    { role: 'user', text: prompt },
    { role: 'assistant', text: `${label} was the earlier answer.` },
    { role: 'user', text: 'Keep that answer available.' },
  ]
}

/**
 * The picker opens an external native session. The next model request must include that session's earlier context.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 *
 * Amp stores threads on its service. The native thread list omits archived and empty threads. The mock seeds external threads. The picker filters them to the working directory. `amp threads continue` resumes the selected thread.
 */
ampTest('offers the workspace\'s Amp threads and resumes the one picked', async ({ page, leapmuxServer, modelScript }) => {
  const { hubUrl, adminToken, workerId, dataDir, mockModelUrl } = leapmuxServer
  const subjectDir = createGitRepo(dataDir, `amp-picker-subject-${crypto.randomUUID()}`)
  const otherDir = createGitRepo(dataDir, `amp-picker-other-${crypto.randomUUID()}`)
  const subjectTree = pathToFileURL(realpathSync(subjectDir)).href
  const seeded = `T-${crypto.randomUUID()}`
  const texts = nativeResumeTexts()
  const priorAnswer = texts.originalAnswer
  await seedThread(mockModelUrl, { id: seeded, title: 'Seeded Amp thread', tree: subjectTree, messages: savedMessages(priorAnswer, texts.originalPrompt) })
  await seedThread(mockModelUrl, { id: `T-${crypto.randomUUID()}`, title: 'Archived Amp thread', tree: subjectTree, messages: savedMessages('Archived'), archived: true })
  await seedThread(mockModelUrl, { id: `T-${crypto.randomUUID()}`, title: 'Empty Amp thread', tree: subjectTree, messages: [] })
  await seedThread(mockModelUrl, { id: `T-${crypto.randomUUID()}`, title: 'Other workspace thread', tree: pathToFileURL(realpathSync(otherDir)).href, messages: savedMessages('Other workspace') })

  // An agent keeps a tab in the workspace, so the New Agent dialog stays reachable.
  const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, `Amp Picker ${crypto.randomUUID()}`)
  const keeperId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, otherDir, {
    ...agentOpenOptions(AgentProvider.AMP),
    title: 'Keeper',
  })
  await loginViaToken(page, adminToken)
  await openWorkspace(page, workspaceId)

  const dialog = await openNewAgentFor(page, AgentProvider.AMP, subjectDir)
  await expect(dialog.getByTestId('agent-provider-selector-trigger')).toContainText('Amp')
  // The one resumable thread of the workspace, under the two rows that are not
  // threads. The archived, the empty and the other workspace's threads are absent.
  const threadRow = await openSoleSessionRow(dialog)
  await expect(menuOptionLabel(threadRow)).toHaveText('Seeded Amp thread')
  await expect(threadRow).toHaveAttribute('data-testid', `loading-menu-option-${seeded}`)
  await createFromSessionRow(dialog, threadRow, seeded)
  const reopened = await expectReopenedNativeAgent({ page, leapmuxServer }, { agentProvider: AgentProvider.AMP, agentSessionId: seeded }, [keeperId])

  // The resumed tab continues the seeded thread: its messages land in that thread.
  const resumedStep = await modelScript.queue({ text: `${ARITHMETIC_ANSWER_TEXT} ${texts.resumedAnswer}` })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps(resumedStep + 1)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  const resumedRequest = await modelScript.requestAt(resumedStep)
  const resumed = JSON.stringify(resumedRequest.body)
  expect(resumed).toContain(priorAnswer)
  expect(resumed).toContain(ARITHMETIC_PROMPT)
  expectNativeResumeContext(nativeModelConversationTurns(resumedRequest), { ...texts, resumedPrompt: ARITHMETIC_PROMPT })
  // An external thread opens without Worker rows to copy, so only the separate resumed answer is provable here.
  await expectResumedAnswerUnmerged({ page, leapmuxServer }, reopened.id, texts)
  const threads = await (await fetch(`${mockModelUrl}${AMP_E2E_THREADS_PATH}`)).json() as AmpThreadView[]
  expect(threads.find(thread => thread.id === seeded)?.messageCount).toBe(5)
})
