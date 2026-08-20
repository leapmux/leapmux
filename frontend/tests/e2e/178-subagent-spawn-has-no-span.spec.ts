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
import { ASSISTANT_BUBBLE_SELECTOR, sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * The Claude Agent tool's result header.
 *
 * The middle part is the TASK title when the payload carries one -- a launch
 * does, quoted and with spaces in it -- and the agent id when it does not, which
 * is the case for a finished synchronous run. The alternation covers both while
 * keeping each side anchored.
 *
 * NOT `.+?`, which this pattern used and which fails in both directions: `.`
 * matches a space, so it crosses out of the header into ordinary assistant prose
 * ("I'll launch the Agent tool and report once it completed"), and that row
 * sits EARLIER in the DOM, so `.first()` picks a plain text row whose
 * data-span-columns is trivially 0 -- the assertion below then passes however
 * the spawn card renders. `.` also does not match a newline, so a model-written
 * title with a line break made the locator find nothing.
 *
 * Inside the quotes: `[\s\S]*?`, LAZY, and not `[^']*`. The title is model prose,
 * so an apostrophe in it is ordinary ("the parser's callers") -- and `[^']*`
 * stops dead at that apostrophe, then demands a status where the next letter
 * sits, so the whole locator matched nothing and the assertion failed red
 * against a card that rendered correctly. `[\s\S]` spans a newline, and the lazy
 * quantifier stops at the FIRST quote that a status follows rather than running
 * to the last quote on the page. `\S+` keeps the bare-id form from crossing a
 * space.
 */
const AGENT_RESULT_HEADER = /Agent (?:'[\s\S]*?'|\S+) (?:completed|failed|launched asynchronously|launched remotely)/
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
    //
    // Restricted to an AGENT row: the prompt above contains the literal
    // "general-purpose", so the user's own message row matches AGENT_TYPE too,
    // and it sits FIRST. Without this filter the assertion reads that row,
    // which never draws a rail, and passes however the spawn card renders.
    const spawnCardRow = page
      .locator('[data-span-columns]:visible')
      .filter({ has: page.locator(ASSISTANT_BUBBLE_SELECTOR) })
      .filter({ hasText: AGENT_TYPE })
      .first()
    await expect(spawnCardRow).toBeVisible()
    await expect(spawnCardRow).toHaveAttribute('data-span-columns', '0')
  })
})
