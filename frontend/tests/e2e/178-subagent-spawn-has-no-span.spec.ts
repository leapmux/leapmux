/**
 * 178 — A subagent spawn draws no span rail.
 *
 * A spawn used to open a span that stayed open for the subagent's whole run, so
 * every concurrent tool was pushed one column right and the transcript filled
 * with deep rails that carried no information. The spawn now owns no span: its
 * tool_use row and its tool_result row draw whatever rail the OTHER open spans
 * draw, and nothing more.
 *
 * The exact geometry is pinned by the Go unit tests (providers/claude/subagent_test.go,
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
 * The spawn is SCRIPTED, so the model no longer has the discretion to decline
 * it and `requireRegistryRow` no longer has a case to skip on.
 */
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import { requireRegistryRow } from './helpers/subagentRegistry'
import { ASSISTANT_BUBBLE_SELECTOR, sendMessage, waitForAgentIdle } from './helpers/ui'

/**
 * The Claude Agent tool's result header.
 *
 * The middle part is the TASK title when the payload carries one -- a launch
 * does, quoted and with spaces in it -- and the agent id when it does not, which
 * is the fallback when neither the result nor the paired tool_use input supplies
 * a description. The alternation covers both while keeping each side anchored.
 *
 * A synchronous result now receives its description from the paired tool_use
 * input, so the quoted form appears for synchronous runs as well.
 *
 * NOT `.+?`, which this pattern used and which fails in both directions: `.`
 * matches a space, so it crosses out of the header into ordinary assistant prose
 * ("I'll launch the Agent tool and report once it completed"), and that row
 * sits EARLIER in the DOM, so `.first()` picks a plain text row whose
 * data-span-columns is trivially 0 -- the assertion below then passes however
 * the spawn card renders. `.` also does not match a newline, so a model-written
 * title with a line break made the locator find nothing.
 *
 * Inside the quotes: `[\s\S]*?`, LAZY, and not `[^"]*`. The title is model
 * prose, so a double quote in it is possible -- and `[^"]*` stops dead at that
 * quote, then demands a status where the next character sits, so the whole
 * locator matched nothing and the assertion failed red against a card that
 * rendered correctly. `[\s\S]` spans a newline, and the lazy quantifier stops at
 * the FIRST quote that a status follows rather than running to the last quote on
 * the page. `\S+` keeps the bare-id form from crossing a space.
 */
const AGENT_RESULT_HEADER = /Agent (?:"[\s\S]*?"|\S+) (?:completed|failed|launched asynchronously|launched remotely)/
/** The Agent tool's own card title, which carries the subagent type. */
const AGENT_TYPE = 'general-purpose'

test.describe('subagent spawn has no span', () => {
  test('the spawn rows draw no rail of their own', async ({ authenticatedWorkspace, page, modelScript }) => {
    void authenticatedWorkspace

    // The spawn is the one tool this turn runs, which is what makes the rail
    // count below unambiguous: a column the spawn rows DID draw could otherwise
    // belong to some other tool the model chose to run beside it.
    const MARKER = 'SPAN-SPAWN-MARKER'
    // How many turns a CHILD runs is the provider's business, not this test's:
    // it summarises, it reports, and each of those is a request the queue never
    // planned for. The fallback answers them so an unplanned turn does not fail
    // a test whose subject is the rail geometry of two rows.
    await modelScript.fallback({ text: `The subagent wrote about the tide and ended with ${MARKER}.` })
    await modelScript.rule({
      name: 'the child writes its sentence',
      when: { user: 'one sentence about the tide' },
      respond: { text: `The tide turns twice a day. ${MARKER}` },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.CLAUDE_CODE, 'spawn-tide', {
        description: 'Write about the tide',
        prompt: modelScript.prompt(`Write one sentence about the tide, then end your reply with the token ${MARKER}.`),
      })],
    })
    await modelScript.queue({ text: `The subagent wrote about the tide and ended with ${MARKER}.` })
    await sendMessage(page, modelScript.prompt('Spawn one general-purpose subagent to write about the tide, then tell me what it wrote.'))
    await modelScript.waitForSteps(2)
    await waitForAgentIdle(page, 180_000)

    // Skips when the model declined to spawn.
    await requireRegistryRow(page)

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
