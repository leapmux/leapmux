/**
 * 174 — Goose subagent registry + tool-request transcript.
 *
 * Goose surfaces tool requests (never results) over ACP via
 * _meta.toolNotification. The row IS clickable (it owns a tool-request
 * transcript). The child also keeps the spawn prompt and final delegate report.
 * Worker-backed: the child agent exists with parent linkage. The child composer
 * is disabled because Goose is not steerable.
 */
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, GOOSE_E2E_SKIP_REASON, gooseTest } from './goose-fixtures'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  exerciseTextGoalQueue,
  expectNoRegistryRows,
  expectRowBecomesFinal,
  expectSectionPersists,
  listAgents,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import { sendMessage } from './helpers/ui'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest.describe('Goose subagent registry', () => {
  gooseTest('queues and observes session-goal commands', async ({
    authenticatedGooseWorkspace,
    page,
  }) => {
    void authenticatedGooseWorkspace
    await exerciseTextGoalQueue(page, {
      objective: 'Wait for the Goose goal route unlock.',
      clearCommand: '/goal off',
    })
  })

  gooseTest('delegate spawn creates a clickable row with a tool-request transcript', async ({
    authenticatedGooseWorkspace,
    page,
    leapmuxServer,
    modelScript,
  }) => {
    void authenticatedGooseWorkspace
    const { hubUrl, adminToken, workerId } = leapmuxServer

    await expectNoRegistryRows(page)

    // The child's prompt carries the marker, so the turns it runs on its own
    // reach this script. The rule matches text the PARENT prompt does not
    // carry, or the parent's own turn would take this answer instead of the
    // queued spawn.
    await modelScript.rule({
      name: 'the child reports the shell result',
      // ANCHORED. A parent turn that carries the tool request embeds this whole
      // prompt, so an unanchored pattern answers the PARENT's turn and the
      // queued step is never consumed. Goose 1.52 opens a subagent's message
      // with a `Subagent ID: <id>` line of its own, which the anchor admits.
      when: { user: '^(?:Subagent ID: [^\\n]*\\n+)?Run `echo goose-done`' },
      respond: { text: 'The command printed goose-done.' },
    })
    // Goose runs a PERMISSION-SAFETY CLASSIFIER turn of its own before it lets a
    // tool run, with its own system prompt. It reads the answer out of a
    // `platform__tool_by_tool_permission` tool call and takes anything else as
    // "not read-only", which holds the tool for an approval that never comes --
    // so the delegate never spawned and no child turn ever reached the script.
    // The test names the request id, so it can also answer for it.
    await modelScript.rule({
      name: 'the permission judge clears the delegate',
      when: { system: 'permission-safety classifier' },
      respond: {
        toolCalls: [{
          id: 'judge-goose',
          name: 'platform__tool_by_tool_permission',
          arguments: { read_only_request_ids: ['spawn-goose'] },
        }],
      },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.GOOSE, 'spawn-goose', {
        description: 'Run the shell probe',
        prompt: modelScript.prompt('Run `echo goose-done` and tell me the result.'),
      })],
    })
    await modelScript.queue({ text: 'The subagent reported goose-done.' })
    await sendMessage(page, modelScript.prompt('Delegate the shell probe to a subagent and report the result.'))
    await modelScript.waitForSteps(2)

    // The spawn is scripted, so a missing row is a failure rather than the
    // model's discretion.
    const r = await requireRegistryRow(page)

    // The spawn itself creates the transcript. A child that uses no tool still
    // keeps its prompt and report. The row renders the attribute on every row, as
    // the empty string until the child exists, so the poll is what proves the id.
    await expect.poll(async () => await r.getAttribute('data-child-agent-id')).not.toBe('')
    const childId = await r.getAttribute('data-child-agent-id') ?? ''

    // Click -> child tab opens adjacent to the parent.
    const tabsBefore = await page.locator('[data-testid="tab"][data-tab-type="agent"]').count()
    await r.click()
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(tabsBefore + 1)
    // Composer on the child tab is disabled (Goose is not steerable), and
    // the box itself says WHY. The placeholder used to blame a lost
    // connection the read-only transcript never had; asserting it here is
    // what proves the reason reaches the editor, since the plugin's unit
    // test cannot see the prop chain that feeds it.
    const noMessages = 'This subagent doesn\'t accept messages.'
    await expect(page.locator(`[data-placeholder="${noMessages}"]:visible`)).toBeVisible()
    // ONCE, not twice. The reason used to render again as a note above the
    // box, so a read-only subagent tab said the same sentence twice, a few
    // pixels apart.
    //
    // VISIBLE only. `Tooltip` also leaves an offscreen `srOnly` description
    // in `aria-describedby` for as long as the control is disabled, which is
    // the only route a screen-reader user has to the reason. An unscoped
    // count reads those too -- nine of them here -- and fails on the
    // behaviour the tooltip is required to have.
    await expect(page.getByText(noMessages, { exact: true }).filter({ visible: true })).toHaveCount(0)

    // The report bubble carries BOTH the label and the child's answer, which is
    // the form 188 already proves. It used to read
    // `getByText('Subagent reported', { exact: true })` behind a status guard:
    // `exact` demands that the element's WHOLE text be those two words, so it
    // could never match a bubble that also carries the report, and the guard
    // kept it from ever running.
    await expect(page.locator('[data-testid="message-bubble"]:visible')
      .filter({ hasText: 'Subagent reported' })
      .filter({ hasText: /goose-done/ })).toBeVisible()

    await expectRowBecomesFinal(page, r)
    await expectSectionPersists(page)

    // Worker-backed: the child agent exists, with its parent linkage.
    // Ask the worker about THIS child id (read off the registry row), the way
    // 170/171 do. Seeding from the hub's tab list instead made this
    // unreachable: tabs live in the user CRDT and the hub's tab projection is
    // empty here, so the id list was always [].
    await expect.poll(async () => {
      const agents = await listAgents(hubUrl, adminToken, workerId, [childId])
      const child = agents?.find(a => a.id === childId)
      if (!child)
        return null
      return {
        hasParent: child.parentAgentId !== '',
        hasSpawnSpan: child.spawnSpanId !== '',
        acceptsMessages: child.acceptsMessages,
      }
    }).toEqual({
      hasParent: true,
      hasSpawnSpan: true,
      // Goose cannot steer a subagent, so the child tab is a read-only
      // transcript -- the same fact the disabled composer above shows.
      acceptsMessages: false,
    })
  })
})
