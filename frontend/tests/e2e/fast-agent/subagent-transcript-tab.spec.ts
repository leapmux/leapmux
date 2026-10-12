import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { fastAgentTest } from '../fastagent-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { reopenFromSessionPicker } from '../helpers/nativeResume'
import { sessionPickerRepository } from '../helpers/nativeResumePicker'
import { nativeAgentById } from '../helpers/nativeScenario'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { backgroundTaskRows, expandBackgroundTasksSection, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, expectRowsInOrder, messageContents, openWorkspace, sendMessage, tabById, toolRows, userBubbles, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { closeNativeAgentAndWait } from '../helpers/workerTabs'
import { openProviderAgent } from '../helpers/workspace'
import { allowReadIfAsked } from './readPermission'
import { FAST_AGENT_AGENT } from './scenarios'

fastAgentTest.describe('Fast Agent subagent transcript', () => {
  const PROVIDER = AgentProvider.FAST_AGENT

  const CHILD_TASK = 'Count the files and report one number.'

  fastAgentTest('opens a new child tab when a cleared session reuses the native call id', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const runs = [
      { task: 'FIRSTFASTCHILDTASK count the first set.', description: 'First Fast child', archive: 'FASTFIRSTARCHIVE', root: 'FASTFIRSTROOT' },
      { task: 'SECONDFASTCHILDTASK count the second set.', description: 'Second Fast child', archive: 'FASTSECONDARCHIVE', root: 'FASTSECONDROOT' },
    ] as const
    let previousChildID = ''
    let previousPrompt = ''
    let previousArchive = ''

    for (const [index, run] of runs.entries()) {
      if (index > 0) {
        await sendMessage(page, '/clear')
        await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()
      }
      const childPrompt = modelScript.prompt(run.task)
      await modelScript.rule({
        name: `fastagent-${index}-child`,
        when: { body: run.task },
        respond: { text: run.archive },
        once: true,
      })
      const start = await modelScript.queue(
        { toolCalls: [spawnSubagentToolCall(PROVIDER, 'fast-reused-child-call', { description: run.description, prompt: childPrompt })] },
        { text: run.root },
      )
      await sendMessage(page, modelScript.prompt(`Delegate ${run.description}.`))
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: run.root }).first()).toBeVisible()

      await expandBackgroundTasksSection(page)
      const row = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: run.description }).first()
      await expect(row).toHaveAttribute('data-status', 'succeeded')
      // `openChildTabFromRow` requires the row to link a child agent, and returns that agent.
      const childID = await openChildTabFromRow(page, row)
      if (previousChildID)
        expect(childID).not.toBe(previousChildID)
      await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()
      await expect(assistantBubbles(page).filter({ hasText: run.archive }).first()).toBeVisible()
      if (previousChildID) {
        await expect(userBubbles(page).filter({ hasText: previousPrompt })).toHaveCount(0)
        await expect(assistantBubbles(page).filter({ hasText: previousArchive })).toHaveCount(0)
      }
      expect((await modelScript.status()).ruleMatches[`fastagent-${index}-child`]).toBe(1)
      previousChildID = childID
      previousPrompt = childPrompt
      previousArchive = run.archive
      await tabById(page, agentId).click()
    }
  })

  fastAgentTest('keeps identical child prompts separate in one session', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const childPrompt = modelScript.prompt(CHILD_TASK)
    const rows = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: 'Count files' })

    await modelScript.rule({
      name: 'the first identical Fast Agent child',
      when: { user: CHILD_TASK },
      respond: { text: 'FASTSAMECHILDONE' },
      once: true,
    })
    const firstStart = await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(PROVIDER, 'fast-same-first', { description: 'Count files', prompt: childPrompt })] },
      { text: 'FASTSAMEROOTONE' },
    )
    await sendMessage(page, modelScript.prompt('Delegate the first count.'))
    await modelScript.waitForSteps(firstStart + 2)
    await waitForAgentIdle(page)
    await expandBackgroundTasksSection(page)
    const firstRow = rows.first()
    await expect(firstRow).toHaveAttribute('data-status', 'succeeded')
    // `openChildTabFromRow` requires the row to link a child agent, and returns that agent.
    const firstChildID = await openChildTabFromRow(page, firstRow)
    await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'FASTSAMECHILDONE' }).first()).toBeVisible()
    await tabById(page, agentId).click()

    await modelScript.rule({
      name: 'the second identical Fast Agent child',
      when: { user: CHILD_TASK },
      respond: { text: 'FASTSAMECHILDTWO' },
      once: true,
    })
    const secondStart = await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(PROVIDER, 'fast-same-second', { description: 'Count files', prompt: childPrompt })] },
      { text: 'FASTSAMEROOTTWO' },
    )
    await sendMessage(page, modelScript.prompt('Delegate the second count with the same task.'))
    await modelScript.waitForSteps(secondStart + 2)
    await waitForAgentIdle(page)

    let secondIndex = -1
    await expect.poll(async () => {
      const childIDs = await Promise.all((await rows.all()).map(async row => await row.getAttribute('data-child-agent-id')))
      secondIndex = childIDs.findIndex(id => !!id && id !== firstChildID)
      return secondIndex
    }).toBeGreaterThanOrEqual(0)
    const secondRow = rows.nth(secondIndex)
    await expect(secondRow).toHaveAttribute('data-status', 'succeeded')
    expect(await openChildTabFromRow(page, secondRow)).not.toBe(firstChildID)
    await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'FASTSAMECHILDTWO' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'FASTSAMECHILDONE' })).toHaveCount(0)

    await tabById(page, firstChildID).click()
    await expect(assistantBubbles(page).filter({ hasText: 'FASTSAMECHILDONE' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'FASTSAMECHILDTWO' })).toHaveCount(0)
    const status = await modelScript.status()
    expect(status.ruleMatches['the first identical Fast Agent child']).toBe(1)
    expect(status.ruleMatches['the second identical Fast Agent child']).toBe(1)
  })

  fastAgentTest('opens the child transcript in its own tab', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, FAST_AGENT_AGENT)
    const note = join(workingDir, 'child-note.txt')
    writeFileSync(note, 'FAST_CHILD_READ_MARKER\n')
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const childPrompt = modelScript.prompt(CHILD_TASK)

    await modelScript.rule(
      {
        name: 'the Fast Agent child reads its note',
        when: { body: CHILD_TASK },
        respond: {
          text: modelScript.prompt('FAST_CHILD_EARLY_TEXT'),
          toolCalls: [readToolCall(PROVIDER, 'fast-child-read', note)],
          gate: 'fast-child-first',
        },
        once: true,
      },
      {
        name: 'the Fast Agent child reports its count',
        when: { body: CHILD_TASK },
        respond: { text: modelScript.prompt('FAST_AGENT_CHILD_DONE') },
        once: true,
      },
    )
    const start = await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(PROVIDER, 'fast-spawn', { description: 'Count files', prompt: childPrompt })] },
      { text: 'FAST_AGENT_ROOT_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Delegate the count, then report.'))
    await modelScript.waitForSteps(start + 1)
    await modelScript.waitForGate('fast-child-first')

    const row = await requireRegistryRow(page)
    // `openChildTabFromRow` waits until the row links a child agent.
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()

    await modelScript.releaseGate('fast-child-first')
    await allowReadIfAsked(page, async () => (await modelScript.status()).ruleMatches['the Fast Agent child reports its count'] === 1)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)

    await expect(assistantBubbles(page).filter({ hasText: 'FAST_AGENT_CHILD_DONE' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'FAST_CHILD_EARLY_TEXT' }).first()).toBeVisible()
    await expect(toolRows(page).filter({ hasText: 'read_text_file' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'FAST_CHILD_READ_MARKER' }).first()).toBeVisible()
    await expectRowsInOrder(assistantBubbles(page), ['FAST_CHILD_EARLY_TEXT', 'FAST_CHILD_READ_MARKER', 'FAST_AGENT_CHILD_DONE'])
    // The row of the Read result holds neither the early text nor the final answer.
    const readResult = assistantBubbles(page).filter({ hasText: 'FAST_CHILD_READ_MARKER' })
    await expect(readResult.filter({ hasText: 'FAST_CHILD_EARLY_TEXT' })).toHaveCount(0)
    await expect(readResult.filter({ hasText: 'FAST_AGENT_CHILD_DONE' })).toHaveCount(0)
    expect((await modelScript.status()).ruleMatches['the Fast Agent child reports its count']).toBe(1)
  })

  fastAgentTest('restores a completed child transcript after its root session reopens', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const workspaceId = authenticatedEmptyWorkspace.workspaceId
    // The root agent reopens through the session picker of its directory.
    const keeperDir = sessionPickerRepository(dataDir, 'fast-child-keeper-')
    const workingDir = sessionPickerRepository(dataDir, 'fast-child-resume-')
    await openAgentViaAPI({ hubUrl, adminToken, workerId }, workspaceId, keeperDir, { title: 'Keeper' })
    const rootID = await openAgentViaAPI({ hubUrl, adminToken, workerId }, workspaceId, workingDir, {
      ...agentOpenOptions(PROVIDER),
      title: 'Subject',
    })
    const note = join(workingDir, 'child-note.txt')
    writeFileSync(note, 'FAST_CHILD_RESUME_TOOL_MARKER\n')
    await openWorkspace(page, workspaceId)
    await tabById(page, rootID).click()
    const childPrompt = modelScript.prompt(CHILD_TASK)

    await modelScript.rule(
      {
        name: 'the resumed Fast Agent child reads its note',
        when: { body: CHILD_TASK },
        respond: {
          text: modelScript.prompt('FAST_CHILD_RESUME_EARLY'),
          toolCalls: [readToolCall(PROVIDER, 'fast-resume-child-read', note)],
          gate: 'fast-resume-child-first',
        },
        once: true,
      },
      {
        name: 'the resumed Fast Agent child reports its result',
        when: { body: CHILD_TASK },
        respond: { text: modelScript.prompt('FAST_CHILD_RESUME_FINAL') },
        once: true,
      },
    )
    const start = await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(PROVIDER, 'fast-resume-spawn', { description: 'Count files', prompt: childPrompt })] },
      { text: 'FAST_ROOT_RESUME_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Delegate the count, then report.'))
    await modelScript.waitForSteps(start + 1)
    await modelScript.waitForGate('fast-resume-child-first')
    const originalRow = await requireRegistryRow(page)
    // `openChildTabFromRow` requires the row to link a child agent, and returns that agent.
    const originalChildID = await openChildTabFromRow(page, originalRow)
    await expect(userBubbles(page).filter({ hasText: childPrompt })).toHaveCount(1)

    await modelScript.releaseGate('fast-resume-child-first')
    await allowReadIfAsked(page, async () => (await modelScript.status()).ruleMatches['the resumed Fast Agent child reports its result'] === 1)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'FAST_CHILD_RESUME_FINAL' })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: 'FAST_CHILD_RESUME_TOOL_MARKER' }).first()).toBeVisible()

    const sessionID = await retryUntilPass(async () => {
      const stored = (await nativeAgentById({ leapmuxServer }, rootID))?.agentSessionId ?? ''
      expect(stored, 'the Worker stores the native session of the root agent').not.toBe('')
      return stored
    })
    await closeNativeAgentAndWait({ leapmuxServer }, rootID)

    await reopenFromSessionPicker(page, { provider: PROVIDER, workingDir, sessionId: sessionID })

    await expect(userBubbles(page).filter({ hasText: 'Delegate the count, then report.' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'FAST_ROOT_RESUME_DONE' })).toHaveCount(1)
    const restoredRow = await requireRegistryRow(page)
    expect(await openChildTabFromRow(page, restoredRow)).not.toBe(originalChildID)
    await expect(userBubbles(page).filter({ hasText: childPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'FAST_CHILD_RESUME_EARLY' })).toHaveCount(1)
    await expect(toolRows(page).filter({ hasText: 'read_text_file' })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: 'FAST_CHILD_RESUME_TOOL_MARKER' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'FAST_CHILD_RESUME_FINAL' })).toHaveCount(1)
    await expectRowsInOrder(assistantBubbles(page), ['FAST_CHILD_RESUME_EARLY', 'FAST_CHILD_RESUME_TOOL_MARKER', 'FAST_CHILD_RESUME_FINAL'])
    // The row of the Read result holds neither the early text nor the final answer.
    const readResult = assistantBubbles(page).filter({ hasText: 'FAST_CHILD_RESUME_TOOL_MARKER' })
    await expect(readResult.filter({ hasText: 'FAST_CHILD_RESUME_EARLY' })).toHaveCount(0)
    await expect(readResult.filter({ hasText: 'FAST_CHILD_RESUME_FINAL' })).toHaveCount(0)
  })
})
