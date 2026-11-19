import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { diracTest } from '../dirac-fixtures'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeAgentById } from '../helpers/nativeScenario'
import { diracRespondToolCall, diracResumeSubagentToolCall, spawnSubagentBatchToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { backgroundTaskRows, expandBackgroundTasksSection, openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, expectRowsInOrder, openWorkspace, sendMessage, tabById, toolCallRow, toolRows, userBubbles, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { diracChildRequestMatcher } from './childRequest'
import { DIRAC_AGENT } from './scenarios'
import { diracTaskletIdFromRecords } from './taskletIdentity'

diracTest.describe('Dirac subagent transcript', () => {
  const PROVIDER = AgentProvider.DIRAC

  const CHILD_TASK = 'Count the files and report one number.'

  diracTest('opens two native children from one spawning call and preserves their separate transcripts', async ({ native }) => {
    const { page, modelScript } = native
    const parent = await currentNativeAgent(native)
    const children = [
      {
        description: 'First batch child',
        prompt: modelScript.prompt('DIRACFIRSTBATCHTASK report the first result.'),
        answer: 'DIRAC_FIRST_BATCH_RESULT',
        gate: 'dirac-first-batch-child',
      },
      {
        description: 'Second batch child',
        prompt: modelScript.prompt('DIRACSECONDBATCHTASK report the second result.'),
        answer: 'DIRAC_SECOND_BATCH_RESULT',
        gate: 'dirac-second-batch-child',
      },
    ] as const
    const parentGate = 'dirac-batch-parent'
    await modelScript.rule(...children.map((child, index) => ({
      name: `dirac-batch-child-${index}`,
      when: diracChildRequestMatcher(child.prompt),
      respond: {
        toolCalls: [
          diracRespondToolCall(`batch-complete-${index}`, 'complete', child.answer),
        ],
        gate: child.gate,
      },
      once: true,
    })))
    const start = await modelScript.queue({
      toolCalls: [
        spawnSubagentBatchToolCall(PROVIDER, 'native-batch-spawn', children),
      ],
    }, {
      toolCalls: [
        diracRespondToolCall('batch-root-complete', 'complete', 'Both native children completed.'),
      ],
      gate: parentGate,
    })
    await withCleanup(async () => {
      await sendMessage(page, modelScript.prompt('Create both scripted child assignments in one native call.'))
      await Promise.all([
        modelScript.waitForGate(children[0].gate),
        modelScript.waitForGate(children[1].gate),
        modelScript.waitForGate(parentGate),
      ])
      await expandBackgroundTasksSection(page)
      await expect(backgroundTaskRows(page, { kind: 'subagent' })).toHaveCount(2)
      const childIDs: string[] = []
      for (const child of children) {
        const row = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: child.description }).first()
        await expect(row).toHaveAttribute('data-status', 'running')
        childIDs.push(await openChildTabFromRow(page, row))
        await expect(userBubbles(page).filter({ hasText: child.prompt }).first()).toBeVisible()
        await tabById(page, parent.id).click()
      }
      expect(childIDs[0]).not.toBe(childIDs[1])
      const [first, second] = await Promise.all(childIDs.map(id => nativeAgentById(native, id)))
      if (!first || !second)
        throw new Error('The Worker must retain both native batch children.')
      expect(first.parentAgentId).toBe(parent.id)
      expect(second.parentAgentId).toBe(parent.id)
      expect(first.agentSessionId).not.toBe('')
      expect(second.agentSessionId).not.toBe(first.agentSessionId)
      expect(first.spawnSpanId).not.toBe('')
      expect(second.spawnSpanId).toBe(first.spawnSpanId)
      const root = await readNativeMessageSnapshot(native, parent.id)
      expect(root.messages.some(message => message.spanId === first.spawnSpanId), 'both children link their actual stored parent call').toBe(true)

      await modelScript.releaseGate(children[0].gate)
      await expect(backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: children[0].description }).first()).toHaveAttribute('data-status', 'succeeded')
      await expect(backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: children[1].description }).first()).toHaveAttribute('data-status', 'running')
      await modelScript.releaseGate(children[1].gate)
      await expect(backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: children[1].description }).first()).toHaveAttribute('data-status', 'succeeded')
      await modelScript.releaseGate(parentGate)
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      for (const [index, child] of children.entries()) {
        const childID = childIDs[index]
        if (!childID)
          throw new Error('The native child tab ID is absent.')
        await tabById(page, childID).click()
        const result = toolCallRow(page, `batch-complete-${index}`, 'result')
        await expect(result).toHaveCount(1)
        await expect(result).toContainText(child.answer)
        await expect(result).toHaveAttribute('data-tool-status', 'completed')
        await page.reload()
        await expect(result).toHaveCount(1)
        await expect(result).toContainText(child.answer)
        const other = children[index === 0
          ? 1
          : 0]
        await expect(assistantBubbles(page).filter({ hasText: other.answer })).toHaveCount(0)
      }
      const status = await modelScript.status()
      expect(status.ruleMatches['dirac-batch-child-0']).toBe(1)
      expect(status.ruleMatches['dirac-batch-child-1']).toBe(1)
    }, () => finishCleanup([
      ...children.map(child => modelScript.releaseGateIfHeld(child.gate)),
      modelScript.releaseGateIfHeld(parentGate),
    ]))
  })

  diracTest('resumes one native tasklet in the same tab while its sibling still runs', async ({ native }) => {
    const { page, modelScript } = native
    const parent = await currentNativeAgent(native)
    const firstTask = modelScript.prompt('DIRACRESUMEFIRSTTASK finish the first assignment.')
    const siblingTask = modelScript.prompt('DIRACRESUMESIBLINGTASK remain running for the proof.')
    const followUp = modelScript.prompt('DIRACRESUMEFOLLOWUP inspect the next assignment.')
    const firstDescription = 'Resumable native child'
    const siblingDescription = 'Held native sibling'
    const gates = [
      'dirac-resume-parent-preview',
      'dirac-resume-parent-final',
      'dirac-resume-sibling',
      'dirac-resume-child-final',
    ] as const
    await modelScript.rule({
      name: 'dirac-resume-first-assignment',
      when: diracChildRequestMatcher(firstTask),
      respond: {
        toolCalls: [
          diracRespondToolCall('first-assignment-complete', 'complete', 'DIRAC_FIRST_ASSIGNMENT_RESULT'),
        ],
      },
      once: true,
    }, {
      name: 'dirac-resume-held-sibling',
      when: diracChildRequestMatcher(siblingTask),
      respond: {
        toolCalls: [
          diracRespondToolCall('sibling-assignment-complete', 'complete', 'DIRAC_SIBLING_RESULT'),
        ],
        gate: gates[2],
      },
      once: true,
    }, {
      name: 'dirac-resumed-assignment',
      when: diracChildRequestMatcher(followUp),
      respond: {
        toolCalls: [
          diracRespondToolCall('resumed-assignment-complete', 'complete', 'DIRAC_RESUMED_ASSIGNMENT_RESULT'),
        ],
        gate: gates[3],
      },
      once: true,
    })
    const start = await modelScript.queue({
      toolCalls: [
        spawnSubagentBatchToolCall(PROVIDER, 'native-resume-batch', [
          { description: firstDescription, prompt: firstTask },
          { description: siblingDescription, prompt: siblingTask },
        ]),
      ],
    }, { text: 'Inspect the completed child before its follow-up.', gate: gates[0] })
    await withCleanup(async () => {
      await sendMessage(page, modelScript.prompt('Create the two scripted assignments, then resume the first one.'))
      await modelScript.waitForGate(gates[0])
      await modelScript.waitForGate(gates[2])
      await expandBackgroundTasksSection(page)
      const firstRow = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: firstDescription }).first()
      const siblingRow = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: siblingDescription }).first()
      await expect(firstRow).toHaveAttribute('data-status', 'succeeded')
      await expect(siblingRow).toHaveAttribute('data-status', 'running')
      const childID = await openChildTabFromRow(page, firstRow)
      const child = await nativeAgentById(native, childID)
      if (!child)
        throw new Error('The Worker must retain the completed native child.')
      const snapshot = await readNativeMessageSnapshot(native, childID)
      const taskletID = diracTaskletIdFromRecords(snapshot.messages.map(nativeMessageBody), parent.agentSessionId, child.agentSessionId)
      await modelScript.queue({
        toolCalls: [
          diracResumeSubagentToolCall('resume-exact-native-tasklet', taskletID, followUp),
        ],
      }, {
        toolCalls: [
          diracRespondToolCall('resume-parent-complete', 'complete', 'Both native assignments completed.'),
        ],
        gate: gates[1],
      })
      await tabById(page, parent.id).click()
      await modelScript.releaseGate(gates[0])
      await modelScript.waitForGate(gates[3])
      await modelScript.waitForGate(gates[1])
      await expect(firstRow).toHaveAttribute('data-status', 'running')
      await expect(firstRow).toHaveAttribute('data-child-agent-id', childID)
      await expect(siblingRow).toHaveAttribute('data-status', 'running')
      const resumed = await nativeAgentById(native, childID)
      expect(resumed?.agentSessionId).toBe(child.agentSessionId)
      await expect(backgroundTaskRows(page, { kind: 'subagent' })).toHaveCount(2)
      await modelScript.releaseGate(gates[3])
      await expect(firstRow).toHaveAttribute('data-status', 'succeeded')
      await modelScript.releaseGate(gates[2])
      await expect(siblingRow).toHaveAttribute('data-status', 'succeeded')
      await modelScript.releaseGate(gates[1])
      await modelScript.waitForSteps(start + 4)
      await waitForAgentIdle(page)
      await tabById(page, childID).click()
      const firstResult = toolCallRow(page, 'first-assignment-complete', 'result')
      const resumedResult = toolCallRow(page, 'resumed-assignment-complete', 'result')
      await expect(firstResult).toHaveCount(1)
      await expect(firstResult).toContainText('DIRAC_FIRST_ASSIGNMENT_RESULT')
      await expect(resumedResult).toHaveCount(1)
      await expect(resumedResult).toContainText('DIRAC_RESUMED_ASSIGNMENT_RESULT')
      await expect(assistantBubbles(page).filter({ hasText: 'DIRAC_SIBLING_RESULT' })).toHaveCount(0)
      await page.reload()
      await expect(firstResult).toHaveCount(1)
      await expect(resumedResult).toHaveCount(1)
      expect((await modelScript.status()).ruleMatches['dirac-resumed-assignment']).toBe(1)
    }, () => finishCleanup(gates.map(gate => modelScript.releaseGateIfHeld(gate))))
  })

  diracTest('opens each child transcript in its own tab', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, DIRAC_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const childPrompt = modelScript.prompt(CHILD_TASK)
    const childAnswer = modelScript.prompt('DIRAC_CHILD_DONE')

    await modelScript.rule(
      {
        name: 'the Dirac child reports its count',
        when: diracChildRequestMatcher(CHILD_TASK),
        respond: {
          text: 'DIRAC_CHILD_ARCHIVE_ONLY',
          toolCalls: [
            diracRespondToolCall('dirac-child-done', 'complete', childAnswer),
          ],
          gate: 'dirac-child-reply',
        },
        once: true,
      },
    )
    const start = await modelScript.queue({
      toolCalls: [
        spawnSubagentToolCall(PROVIDER, 'dirac-spawn', { description: 'Count files', prompt: childPrompt }),
      ],
    }, {
      toolCalls: [
        diracRespondToolCall('dirac-root-done', 'complete', 'DIRAC_ROOT_DONE'),
      ],
      gate: 'dirac-parent-reply',
    })
    await withCleanup(async () => {
      await sendMessage(page, modelScript.prompt('Delegate the count, then report.'))
      await modelScript.waitForGate('dirac-child-reply')
      await modelScript.waitForGate('dirac-parent-reply')

      await expandBackgroundTasksSection(page)
      const child = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: 'Count files' }).first()
      await expect(child).toBeVisible()
      // `openChildTabFromRow` waits until the row links a child agent.
      await openChildTabFromRow(page, child)
      await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()

      await modelScript.releaseGate('dirac-child-reply')
      await expect(child).toHaveAttribute('data-status', 'succeeded')
      await modelScript.releaseGate('dirac-parent-reply')
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: 'DIRAC_CHILD_DONE' }).first()).toBeVisible()
      await expect(assistantBubbles(page).filter({ hasText: 'DIRAC_CHILD_ARCHIVE_ONLY' }).first()).toBeVisible()
      await expect(toolRows(page).filter({ hasText: 'DIRAC_CHILD_DONE' }).first()).toBeVisible()
      await expectRowsInOrder(assistantBubbles(page), ['DIRAC_CHILD_ARCHIVE_ONLY', 'DIRAC_CHILD_DONE'])
      expect((await modelScript.status()).ruleMatches['the Dirac child reports its count']).toBe(1)
    }, () => finishCleanup([
      modelScript.releaseGateIfHeld('dirac-child-reply'),
      modelScript.releaseGateIfHeld('dirac-parent-reply'),
    ]))
  })

  diracTest('opens a new child tab when a cleared session reuses the native call id', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, DIRAC_AGENT)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    const runs = [
      {
        task: 'FIRSTCHILDTASK count the first set.',
        description: 'First child count',
        archive: 'DIRACFIRSTARCHIVE',
        report: 'DIRACFIRSTREPORT',
        root: 'DIRACFIRSTROOT',
      },
      {
        task: 'SECONDCHILDTASK count the second set.',
        description: 'Second child count',
        archive: 'DIRACSECONDARCHIVE',
        report: 'DIRACSECONDREPORT',
        root: 'DIRACSECONDROOT',
      },
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
      await modelScript.rule(
        {
          name: `dirac-${index}-child`,
          when: diracChildRequestMatcher(run.task),
          respond: {
            gate: `dirac-${index}-child-reply`,
            text: run.archive,
            toolCalls: [
              diracRespondToolCall(`dirac-${index}-child-done`, 'complete', modelScript.prompt(run.report)),
            ],
          },
          once: true,
        },
      )
      const start = await modelScript.queue({
        toolCalls: [
          spawnSubagentToolCall(PROVIDER, 'dirac-reused-child-call', { description: run.description, prompt: childPrompt }),
        ],
      }, {
        toolCalls: [
          diracRespondToolCall(`dirac-${index}-root-done`, 'complete', run.root),
        ],
        gate: `dirac-${index}-parent-reply`,
      })
      await withCleanup(async () => {
        await sendMessage(page, modelScript.prompt(`Delegate ${run.description}.`))
        await modelScript.waitForGate(`dirac-${index}-child-reply`)
        await modelScript.waitForGate(`dirac-${index}-parent-reply`)

        await expandBackgroundTasksSection(page)
        const row = backgroundTaskRows(page, { kind: 'subagent' }).filter({ hasText: run.description }).first()
        await modelScript.releaseGate(`dirac-${index}-child-reply`)
        await expect(row).toHaveAttribute('data-status', 'succeeded')
        await modelScript.releaseGate(`dirac-${index}-parent-reply`)
        await modelScript.waitForSteps(start + 2)
        await waitForAgentIdle(page)
        // `openChildTabFromRow` requires the row to link a child agent, and returns that agent.
        const childID = await openChildTabFromRow(page, row)
        if (previousChildID)
          expect(childID).not.toBe(previousChildID)
        await expect(userBubbles(page).filter({ hasText: childPrompt }).first()).toBeVisible()
        await expect(assistantBubbles(page).filter({ hasText: run.archive }).first()).toBeVisible()
        await expect(assistantBubbles(page).filter({ hasText: run.report }).first()).toBeVisible()
        if (previousChildID) {
          await expect(userBubbles(page).filter({ hasText: previousPrompt })).toHaveCount(0)
          await expect(assistantBubbles(page).filter({ hasText: previousArchive })).toHaveCount(0)
        }
        await expectRowsInOrder(assistantBubbles(page), [run.archive, run.report])
        const status = await modelScript.status()
        expect(status.ruleMatches[`dirac-${index}-child`]).toBe(1)
        previousChildID = childID
        previousPrompt = childPrompt
        previousArchive = run.archive
        await tabById(page, agentId).click()
      }, () => finishCleanup([
        modelScript.releaseGateIfHeld(`dirac-${index}-child-reply`),
        modelScript.releaseGateIfHeld(`dirac-${index}-parent-reply`),
      ]))
    }
  })
})
