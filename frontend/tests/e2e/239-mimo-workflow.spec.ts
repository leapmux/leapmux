import type { Locator, Page } from '@playwright/test'
import { mimoWorkflowToolCall } from './helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal, openChildTabFromRow } from './helpers/subagentRegistry'
import { assistantBubbles, bandRows, messageBubbles, messageContents, sendMessage, tabById, userBubbles, waitForAgentIdle } from './helpers/ui'
/**
 * 239 — MiMo Code workflow.
 *
 * MiMo's experimental `workflow` tool runs a script that spawns subagents in the
 * parent's own session. The worker turns the run into one workflow row of the
 * subagent registry, groups the rows of the subagents that the run spawns under
 * it, and gives each subagent a transcript of its own.
 */
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

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

/**
 * The text of the group heading above one registry row, or null for a row in no
 * group.
 *
 * The list draws each ungrouped row first, and then each group as its heading
 * followed by its rows, all as siblings. So the nearest sibling above a row that
 * is not a row itself is the heading of that row's group.
 */
async function groupHeadingOf(row: Locator): Promise<string | null> {
  return row.evaluate((element) => {
    let sibling = element.previousElementSibling
    while (sibling && sibling.getAttribute('data-testid') === 'bg-task-row')
      sibling = sibling.previousElementSibling
    return sibling?.textContent?.trim() ?? null
  })
}

mimoTest.describe('MiMo Code workflow', () => {
  mimoTest('a workflow run shows its subagents in the registry, each in a transcript of its own', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    const helpers = HELPERS.map(helper => ({ ...helper, prompt: modelScript.prompt(helperTask(helper.word)) }))

    // Each subagent's turn is a RULE: the two run in parallel, so their requests
    // arrive in no fixed order. A rule is ANCHORED at the start of the subagent's
    // task, because the parent's second request also quotes each answer, inside
    // MiMo's notification that the subagent finished. The answer takes the report
    // form that MiMo asks its subagents for.
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
    await waitForAgentIdle(page, 120_000)

    // The end of the run: the parent answered after the workflow returned, and the
    // workflow call closed with MiMo's own title for a completed run.
    await expect(assistantBubbles(page).filter({ hasText: 'WORKFLOW_DONE' })).toBeVisible()
    await expect(messageContents(page).filter({ hasText: 'workflow inline completed' }).first()).toBeVisible()
    // Each subagent ran in the background, so its answer reaches the parent as a
    // report, and never as an answer text of the parent's own. The workflow call's
    // result quotes both answers too, and it is a tool row, not a text row.
    for (const { label, word } of helpers) {
      await expect(messageBubbles(page).filter({ hasText: `${label} reported` }).filter({ hasText: word })).toBeVisible()
      await expect(bandRows(page, 'text').filter({ hasText: word })).toHaveCount(0)
    }

    // The registry: one row for the run, closed as completed, with the run's name
    // as its title and as the heading of its group.
    await expandBackgroundTasksSection(page)
    const workflowRow = registryRow(page, 'workflow', WORKFLOW_NAME)
    await expectRowBecomesFinal(page, workflowRow)
    await expect(workflowRow).toHaveAttribute('data-status', 'completed')
    await expect.poll(() => groupHeadingOf(workflowRow)).toContain(WORKFLOW_NAME)

    for (const [index, { label, word }] of helpers.entries()) {
      // One row for each subagent, in the run's group, closed as completed, and
      // linked to a transcript of its own.
      const row = registryRow(page, 'subagent', label)
      await expectRowBecomesFinal(page, row)
      await expect(row).toHaveAttribute('data-status', 'completed')
      await expect.poll(() => groupHeadingOf(row)).toContain(WORKFLOW_NAME)
      // `getAttribute` answers null for an absent attribute, and null is not '', so
      // the poll reads an absent attribute as the empty id it states.
      await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')

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
