import type { Locator, Page } from '@playwright/test'
/**
 * The actual native workflow creates a workflow row and grouped child rows. Each child keeps its own transcript.
 *
 * The Worker drives MiMo Code's native HTTP server. MiMo identifies each child actor in its events.
 *
 * MiMo's workflow script spawns child actors inside the parent session. The Worker groups their registry rows under the native workflow row.
 */
import { expect } from '@playwright/test'
import { escapeRegExp } from '../../../src/lib/regexp'
import { mimoWorkflowToolCall } from '../helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal, openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, bandRows, messageContents, sendMessage, subagentReportBubble, tabById, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { expectRowsInWorkflowGroup } from '../helpers/workflowGrouping'
import { mimoTest } from '../mimo-fixtures'

/** The run's name, from the script's `meta`. The registry titles the run and its group with it. */
const WORKFLOW_NAME = 'leapmux-e2e-words'

/** The phase the script enters before it spawns its subagents. */
const WORKFLOW_PHASE = 'Ask'

/**
 * The subagents of the run. Each one gets a one-word task, and the label is the
 * title that MiMo gives the subagent, which its registry row and its report state.
 */
const HELPERS = [
  { label: 'Alpha helper', word: 'ALPHA_WORD' },
  { label: 'Beta helper', word: 'BETA_WORD' },
] as const

/** The one-word task of a subagent, before the test marks it as its own. */
function helperTask(word: string): string {
  return `Reply with the single word ${word}.`
}

/**
 * The workflow script: one phase, then both subagents in parallel through
 * `agent()`, and their answers as the run's result.
 *
 * `meta` must open the script and must be a data literal, and MiMo reads its
 * `name` as the run's name. Each prompt is a JSON string literal, so the marker
 * that `modelScript.prompt` appends reaches the subagent unchanged. The marker is
 * what routes the subagent's own model requests to this test's script.
 */
function workflowScript(prompts: readonly { label: string, prompt: string }[]): string {
  return [
    `export const meta = { name: ${JSON.stringify(WORKFLOW_NAME)}, description: "Ask two subagents for one word each." }`,
    `phase(${JSON.stringify(WORKFLOW_PHASE)})`,
    'const words = await parallel([',
    ...prompts.map(({ label, prompt }) => `  () => agent(${JSON.stringify(prompt)}, { label: ${JSON.stringify(label)} }),`),
    '])',
    'return words',
  ].join('\n')
}

/** The visible registry row of one kind whose title holds `title`. */
function registryRow(page: Page, kind: 'subagent' | 'workflow', title: string): Locator {
  // `:visible` plus `.first()`: the sidebar is mounted twice.
  return page.locator(`[data-testid="bg-task-row"]:visible[data-kind="${kind}"]`).filter({ hasText: title }).first()
}

mimoTest.describe('MiMo Code workflow', () => {
  mimoTest('a workflow run shows its subagents in the registry, each in a transcript of its own', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    const helpers = HELPERS.map(helper => ({ ...helper, prompt: modelScript.prompt(helperTask(helper.word)) }))

    // The two children run in parallel, so their requests have no fixed order. Match each child through a model rule.
    // Anchor the rule at the start of its task. The parent notification also quotes each completed answer.
    // Return the report format that MiMo requests from its children.
    await modelScript.rule(...helpers.map(({ label, word }) => ({
      name: `the ${label} answers its one-word task`,
      when: { user: `^${helperTask(word).replace('.', '\\.')}` },
      respond: { text: `**Status**: success\n**Summary**: replied\n\n${word}` },
    })))
    // The parent runs the workflow, which blocks until both subagents answer, and
    // then answers once itself. MiMo delivers each subagent's notification into
    // that same turn, so no second parent turn starts.
    await modelScript.queue(
      { toolCalls: [mimoWorkflowToolCall('workflow-run', workflowScript(helpers))] },
      { text: 'WORKFLOW_DONE: both helpers answered.' },
    )
    const parentTabId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
    expect(parentTabId).not.toBe('')
    await sendMessage(page, modelScript.prompt('Run the one-word workflow and report what it returned.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The end of the run: the parent answered after the workflow returned, and the
    // workflow call closed with MiMo's own title for a completed run.
    await expect(assistantBubbles(page).filter({ hasText: 'WORKFLOW_DONE' })).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'workflow inline completed' }).first()).toBeVisible()
    // Each subagent ran in the background, so its answer reaches the parent as a
    // report, and never as an answer text of the parent's own. The workflow call's
    // result quotes both answers too, and it is a tool row, not a text row.
    for (const { label, word } of helpers) {
      await expect(subagentReportBubble(page, word, label)).toBeVisible()
      await expect(bandRows(page, 'text').filter({ hasText: word })).toHaveCount(0)
    }

    // The registry: one row for the run and one row for each subagent, each closed as
    // completed, and all in the group whose heading holds the run's name.
    await expandBackgroundTasksSection(page)
    const workflowRow = registryRow(page, 'workflow', WORKFLOW_NAME)
    const helperRows = helpers.map(helper => ({ ...helper, row: registryRow(page, 'subagent', helper.label) }))
    const rows = [workflowRow, ...helperRows.map(({ row }) => row)]
    for (const row of rows) {
      await expectRowBecomesFinal(page, row)
      await expect(row).toHaveAttribute('data-status', 'completed')
    }
    await expectRowsInWorkflowGroup(rows, new RegExp(escapeRegExp(WORKFLOW_NAME)))

    for (const [index, { word, row }] of helperRows.entries()) {
      // Each subagent row links a transcript of its own.
      if (index > 0)
        await tabById(page, parentTabId).click()
      await openChildTabFromRow(page, row)
      // The transcript opens on the subagent's task, as MiMo received it from the
      // script, without the return-format instruction that MiMo appends to it.
      const task = userBubbles(page).filter({ hasText: helperTask(word) })
      await expect(task).toBeVisible()
      await expect(task).not.toContainText('Return format')
      // The subagent's own answer is a text row of its own transcript, and the
      // parent's answer is not.
      await expect(bandRows(page, 'text').filter({ hasText: word })).toBeVisible()
      await expect(bandRows(page, 'text').filter({ hasText: 'WORKFLOW_DONE' })).toHaveCount(0)
    }
  })
})
