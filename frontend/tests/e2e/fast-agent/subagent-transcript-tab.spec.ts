import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { expect, fastAgentTest, openFastAgentAgent } from '../fastagent-fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { readToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expandBackgroundTasksSection, listAgents, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, messageContents, openMenu, openWorkspace, sendMessage, tabById, userBubbles, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { closeAgentViaAPI, createGitRepo, openNewAgentDialog, setWorkingDir, waitForWorker } from '../helpers/worktree'

fastAgentTest.describe('Fast Agent subagent transcript', () => {
  const PROVIDER = AgentProvider.FAST_AGENT

  const CHILD_TASK = 'Count the files and report one number.'

  fastAgentTest('opens a new child tab when a cleared session reuses the native call id', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
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
      await modelScript.queue(
        { toolCalls: [spawnSubagentToolCall(PROVIDER, 'fast-reused-child-call', { description: run.description, prompt: childPrompt })] },
        { text: run.root },
      )
      await sendMessage(page, modelScript.prompt(`Delegate ${run.description}.`))
      await modelScript.waitForSteps()
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: run.root }).first()).toBeVisible()

      await expandBackgroundTasksSection(page)
      const row = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]').filter({ hasText: run.description }).first()
      await expect(row).toHaveAttribute('data-status', 'completed')
      const childID = await row.getAttribute('data-child-agent-id')
      expect(childID).toBeTruthy()
      if (previousChildID)
        expect(childID).not.toBe(previousChildID)
      await openChildTabFromRow(page, row)
      await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()
      await expect(assistantBubbles(page).filter({ hasText: run.archive }).first()).toBeVisible()
      if (previousChildID) {
        await expect(userBubbles(page).filter({ hasText: previousPrompt })).toHaveCount(0)
        await expect(assistantBubbles(page).filter({ hasText: previousArchive })).toHaveCount(0)
      }
      expect((await modelScript.status()).ruleMatches[`fastagent-${index}-child`]).toBe(1)
      previousChildID = childID ?? ''
      previousPrompt = childPrompt
      previousArchive = run.archive
      await tabById(page, agentId).click()
    }
  })

  fastAgentTest('keeps identical child prompts separate in one session', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const childPrompt = modelScript.prompt(CHILD_TASK)
    const rows = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]').filter({ hasText: 'Count files' })

    await modelScript.rule({
      name: 'the first identical Fast Agent child',
      when: { user: CHILD_TASK },
      respond: { text: 'FASTSAMECHILDONE' },
      once: true,
    })
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(PROVIDER, 'fast-same-first', { description: 'Count files', prompt: childPrompt })] },
      { text: 'FASTSAMEROOTONE' },
    )
    await sendMessage(page, modelScript.prompt('Delegate the first count.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expandBackgroundTasksSection(page)
    const firstRow = rows.first()
    await expect(firstRow).toHaveAttribute('data-status', 'completed')
    const firstChildID = await firstRow.getAttribute('data-child-agent-id')
    if (!firstChildID)
      throw new Error('the first Fast Agent child row has no agent ID')
    const firstChildTabID = await openChildTabFromRow(page, firstRow)
    await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'FASTSAMECHILDONE' }).first()).toBeVisible()
    await tabById(page, agentId).click()

    await modelScript.rule({
      name: 'the second identical Fast Agent child',
      when: { user: CHILD_TASK },
      respond: { text: 'FASTSAMECHILDTWO' },
      once: true,
    })
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(PROVIDER, 'fast-same-second', { description: 'Count files', prompt: childPrompt })] },
      { text: 'FASTSAMEROOTTWO' },
    )
    await sendMessage(page, modelScript.prompt('Delegate the second count with the same task.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    let secondIndex = -1
    await expect.poll(async () => {
      const childIDs = await Promise.all((await rows.all()).map(async row => await row.getAttribute('data-child-agent-id')))
      secondIndex = childIDs.findIndex(id => !!id && id !== firstChildID)
      return secondIndex
    }).toBeGreaterThanOrEqual(0)
    const secondRow = rows.nth(secondIndex)
    await expect(secondRow).toHaveAttribute('data-status', 'completed')
    const secondChildID = await secondRow.getAttribute('data-child-agent-id')
    expect(secondChildID).toBeTruthy()
    expect(secondChildID).not.toBe(firstChildID)
    await openChildTabFromRow(page, secondRow)
    await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'FASTSAMECHILDTWO' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'FASTSAMECHILDONE' })).toHaveCount(0)

    await tabById(page, firstChildTabID).click()
    await expect(assistantBubbles(page).filter({ hasText: 'FASTSAMECHILDONE' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'FASTSAMECHILDTWO' })).toHaveCount(0)
    const status = await modelScript.status()
    expect(status.ruleMatches['the first identical Fast Agent child']).toBe(1)
    expect(status.ruleMatches['the second identical Fast Agent child']).toBe(1)
  })

  fastAgentTest('opens the child transcript in its own tab', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openFastAgentAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
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
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(PROVIDER, 'fast-spawn', { description: 'Count files', prompt: childPrompt })] },
      { text: 'FAST_AGENT_ROOT_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Delegate the count, then report.'))
    await modelScript.waitForSteps(1)
    await modelScript.waitForGate('fast-child-first')

    const row = await requireRegistryRow(page)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()

    await modelScript.releaseGate('fast-child-first')
    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect.poll(async () => {
      if (await banner.isVisible())
        return 'permission'
      return (await modelScript.status()).ruleMatches['the Fast Agent child reports its count'] === 1 ? 'continued' : 'waiting'
    }).not.toBe('waiting')
    if (await banner.isVisible()) {
      await expect(banner).toContainText('read_text_file')
      await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    }
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expect(assistantBubbles(page).filter({ hasText: 'FAST_AGENT_CHILD_DONE' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'FAST_CHILD_EARLY_TEXT' }).first()).toBeVisible()
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'read_text_file' }).first()).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'FAST_CHILD_READ_MARKER' }).first()).toBeVisible()
    const answers = await assistantBubbles(page).allTextContents()
    const early = answers.findIndex(text => text.includes('FAST_CHILD_EARLY_TEXT'))
    const result = answers.findIndex(text => text.includes('FAST_CHILD_READ_MARKER'))
    const final = answers.findIndex(text => text.includes('FAST_AGENT_CHILD_DONE'))
    expect(early).toBeGreaterThanOrEqual(0)
    expect(result).toBeGreaterThan(early)
    expect(final).toBeGreaterThan(result)
    expect(answers[result]).not.toContain('FAST_CHILD_EARLY_TEXT')
    expect(answers[result]).not.toContain('FAST_AGENT_CHILD_DONE')
    expect((await modelScript.status()).ruleMatches['the Fast Agent child reports its count']).toBe(1)
  })

  fastAgentTest('restores a completed child transcript after its root session reopens', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const workspaceId = authenticatedEmptyWorkspace.workspaceId
    const keeperDir = createGitRepo(dataDir, `fast-child-keeper-${crypto.randomUUID()}`)
    const workingDir = createGitRepo(dataDir, `fast-child-resume-${crypto.randomUUID()}`)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, keeperDir, { title: 'Keeper' })
    const rootID = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, workingDir, {
      agentProvider: PROVIDER,
      ...agentOpenOptions(agentSettings(PROVIDER)),
      title: 'Subject',
    })
    const note = join(workingDir, 'child-note.txt')
    writeFileSync(note, 'FAST_CHILD_RESUME_TOOL_MARKER\n')
    await openWorkspace(page, workspaceId)
    await page.locator('[data-testid="tab"][data-tab-type="agent"]').filter({ hasText: 'Subject' }).first().click()
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
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(PROVIDER, 'fast-resume-spawn', { description: 'Count files', prompt: childPrompt })] },
      { text: 'FAST_ROOT_RESUME_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Delegate the count, then report.'))
    await modelScript.waitForSteps(1)
    await modelScript.waitForGate('fast-resume-child-first')
    const originalRow = await requireRegistryRow(page)
    const originalChildID = await originalRow.getAttribute('data-child-agent-id')
    expect(originalChildID).toBeTruthy()
    await openChildTabFromRow(page, originalRow)
    await expect(userBubbles(page).filter({ hasText: childPrompt })).toHaveCount(1)

    await modelScript.releaseGate('fast-resume-child-first')
    const banner = page.locator('[data-testid="control-banner"]:visible')
    await expect.poll(async () => {
      if (await banner.isVisible())
        return 'permission'
      return (await modelScript.status()).ruleMatches['the resumed Fast Agent child reports its result'] === 1 ? 'continued' : 'waiting'
    }).not.toBe('waiting')
    if (await banner.isVisible()) {
      await expect(banner).toContainText('read_text_file')
      await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    }
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'FAST_CHILD_RESUME_FINAL' })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: 'FAST_CHILD_RESUME_TOOL_MARKER' }).first()).toBeVisible()

    let sessionID = ''
    await expect.poll(async () => {
      const agents = await listAgents(hubUrl, adminToken, workerId, [rootID])
      sessionID = agents?.find(agent => agent.id === rootID)?.agentSessionId ?? ''
      return sessionID
    }).not.toBe('')
    await closeAgentViaAPI(hubUrl, adminToken, workerId, rootID)

    await openNewAgentDialog(page)
    await waitForWorker(page)
    const dialog = page.getByRole('dialog')
    await dialog.getByTestId('agent-provider-selector-trigger').click()
    await page.getByTestId(`agent-provider-option-${PROVIDER}`).click()
    await setWorkingDir(page, workingDir)
    await openMenu(dialog, 'session-select-menu')
    const session = dialog.getByTestId('session-select-menu').getByTestId(`loading-menu-option-${sessionID}`)
    await expect(session).toBeVisible()
    await session.click()
    await dialog.getByRole('button', { name: 'Create' }).click()

    await expect(userBubbles(page).filter({ hasText: 'Delegate the count, then report.' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'FAST_ROOT_RESUME_DONE' })).toHaveCount(1)
    const restoredRow = await requireRegistryRow(page)
    const restoredChildID = await restoredRow.getAttribute('data-child-agent-id')
    expect(restoredChildID).toBeTruthy()
    expect(restoredChildID).not.toBe(originalChildID)
    await openChildTabFromRow(page, restoredRow)
    await expect(userBubbles(page).filter({ hasText: childPrompt })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'FAST_CHILD_RESUME_EARLY' })).toHaveCount(1)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'read_text_file' })).toHaveCount(1)
    await expect(messageContents(page).filter({ hasText: 'FAST_CHILD_RESUME_TOOL_MARKER' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'FAST_CHILD_RESUME_FINAL' })).toHaveCount(1)
    const answers = await assistantBubbles(page).allTextContents()
    const early = answers.findIndex(text => text.includes('FAST_CHILD_RESUME_EARLY'))
    const result = answers.findIndex(text => text.includes('FAST_CHILD_RESUME_TOOL_MARKER'))
    const final = answers.findIndex(text => text.includes('FAST_CHILD_RESUME_FINAL'))
    expect(early).toBeGreaterThanOrEqual(0)
    expect(result).toBeGreaterThan(early)
    expect(final).toBeGreaterThan(result)
    expect(answers[result]).not.toContain('FAST_CHILD_RESUME_EARLY')
    expect(answers[result]).not.toContain('FAST_CHILD_RESUME_FINAL')
  })
})
