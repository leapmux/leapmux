/**
 * 178 — A subagent spawn draws no span rail.
 *
 * A spawn used to open a span that stayed open for the subagent's whole run, so
 * every concurrent tool was pushed one column right and the transcript filled
 * with deep rails that carried no information. The spawn now owns no span: its
 * tool_use row and its tool_result row draw whatever rail the OTHER open spans
 * draw, and nothing more.
 *
 * The exact geometry is pinned by the Go unit tests (claude_subagent_test.go,
 * span_tracker_test.go, output_spawn_span_lines_test.go), which script the
 * envelope order directly. This spec is the whole-stack smoke test: it drives a
 * REAL Claude CLI, so it asserts only what holds however the model behaves —
 * that the spawn rows carry no rail of their own, and that an ordinary tool in
 * the same transcript still draws one.
 *
 * data-span-columns is the rail count of one row ("0" when it draws none). Every
 * span-line column class is a hashed vanilla-extract name, so it is the only
 * stable hook for this.
 *
 * The model may decline to spawn at all, which is its discretion rather than a
 * defect, so requireRegistryRow skips instead of failing.
 */
import { expect, test } from './fixtures'
import { requireRegistryRow } from './helpers/subagentRegistry'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

/** The Claude Agent tool's result header: "Agent <id> completed". */
const AGENT_RESULT_HEADER = /Agent \S+ (completed|failed|launched asynchronously)/
/** The Agent tool's own card title, which carries the subagent type. */
const AGENT_TYPE = 'general-purpose'

test.describe('subagent spawn has no span', () => {
  test('the spawn rows draw no rail of their own', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace

    // Directive about the TOOL as well as the outcome: spec 170 found that a
    // task the model can shortcut with Bash produces a shell row and covers
    // nothing. Writing prose is something Bash cannot do.
    const MARKER = 'SPAN-SPAWN-MARKER'
    await sendMessage(page, `You MUST use the Task tool. Spawn exactly one general-purpose subagent and give it this prompt verbatim: "Write one sentence about the tide, then end your reply with the token ${MARKER}." Do not answer it yourself and do not use Bash. Wait for the subagent to finish, then tell me what it wrote.`)
    await waitForAgentIdle(page, 180_000)

    // Skips when the model declined to spawn.
    await requireRegistryRow(test, page)

    // Rows are scoped to :visible — ChatView renders every unmeasured row twice
    // and the sidebar is mounted twice, so an unscoped locator picks the wrong
    // copy.
    const spawnResultRow = page
      .locator('[data-span-columns]:visible')
      .filter({ hasText: AGENT_RESULT_HEADER })
      .first()
    await expect(spawnResultRow).toBeVisible()

    // The spawn's own result draws no rail. Any column it DOES show would have
    // to come from another tool that is still running, and this prompt asks for
    // no other tool.
    await expect(spawnResultRow).toHaveAttribute('data-span-columns', '0')

    // Its tool_use card -- the row titled with the subagent type -- draws none
    // either. Before the change this row was the one that opened the rail.
    const spawnCardRow = page
      .locator('[data-span-columns]:visible')
      .filter({ hasText: AGENT_TYPE })
      .first()
    await expect(spawnCardRow).toBeVisible()
    await expect(spawnCardRow).toHaveAttribute('data-span-columns', '0')
  })
})
