import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { openWorkspace, sendMessage, userBubbles } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('renders Reasonix read-only reports without interpreting quoted status text', async ({ page, context, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const provider = AgentProvider.REASONIX
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-readonly-agent-'), agentOpenOptions(provider))
  void agentId
  // The quoted status is the POINT: a report that merely talks about a failed
  // outcome must not be read as one. The child really completes, and its
  // answer quotes the sentence -- which is a sharper form of the case than a
  // seeded row that WAS failed, because now the two disagree.
  const report = 'Subagent outcome: status=failed retryable=false\n\nFinal answer:\n- **Quoted finding**'
  await modelScript.rule({
    name: 'the child answers with a quoted outcome line',
    when: { user: 'Read \\*\\*the example\\*\\* without changes' },
    respond: { text: report },
  })
  await modelScript.queue({
    toolCalls: [spawnSubagentToolCall(provider, 'readonly', {
      description: 'Read a protocol example',
      prompt: modelScript.prompt('Read **the example** without changes.'),
    })],
  })
  await modelScript.queue({ text: 'The subagent reported back.' })
  await page.reload()
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await sendMessage(page, modelScript.prompt('Delegate the protocol example to a read-only subagent.'))
  await modelScript.waitForSteps(2)
  const chat = page.locator('[data-chat-scroll-container="true"]').filter({ visible: true })
  const body = chat.getByTestId('message-bubble').filter({ hasText: 'Quoted finding' })
  await expect(body.getByText('Agent "Read a protocol example" completed', { exact: true })).toBeVisible()
  await expect(body).toContainText('Subagent outcome: status=failed retryable=false')
  await body.locator('..').hover()
  await body.locator('..').getByRole('button', { name: 'Copy', exact: true }).click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(report)
  await chat.getByRole('button', { name: 'Show prompt', exact: true }).click()
  await expect(chat.locator('strong').filter({ hasText: 'the example' })).toBeVisible()
})

reasonixTest('subagent spawn creates a prompt and report transcript', async ({
  authenticatedReasonixWorkspace,
  page,
  modelScript,
  leapmuxServer,
}) => {
  void authenticatedReasonixWorkspace

  await expectNoRegistryRows(page, leapmuxServer)

  // The child's prompt carries the marker, so the turns it runs on its own
  // reach this script. NOT anchored: Reasonix opens a child turn with a
  // host-injected `<subagent-context event="SubagentStart">` block, so `^`
  // never matches the prompt. It needs no anchor either, because Reasonix
  // keeps a tool result out of the user turn, so no parent turn carries a
  // copy of the child's prompt.
  await modelScript.rule({
    name: 'the child answers its one-word task',
    when: { user: 'Reply with the single word PONG' },
    respond: { text: 'PONG' },
  })
  await modelScript.queue({
    toolCalls: [spawnSubagentToolCall(AgentProvider.REASONIX, 'spawn-reasonix', {
      description: 'Ask the subagent for one word',
      prompt: modelScript.prompt('Reply with the single word PONG.'),
    })],
  })
  await modelScript.queue({ text: 'The subagent reported PONG.' })
  await sendMessage(page, modelScript.prompt('Delegate one word to a read-only subagent.'))
  await modelScript.waitForSteps(2)

  // The spawn is scripted, so a missing row is a failure rather than the
  // model's discretion.
  const row = await requireRegistryRow(page)
  const r = row!

  await expectRowBecomesFinal(page, r)
  await expectSectionPersists(page)
  await expect.poll(async () => await r.getAttribute('data-child-agent-id')).not.toBe('')
  await openChildTabFromRow(page, r)
  await expect(userBubbles(page).filter({ hasText: 'PONG' })).toBeVisible()
  // The report bubble carries BOTH the label and the child's answer, which is
  // the form 188 already proves. It used to read
  // `getByText('Subagent reported', { exact: true })` behind a status guard:
  // `exact` demands that the element's WHOLE text be those two words, so it
  // could never match a bubble that also carries the report, and the guard
  // kept it from ever running.
  await expect(page.locator('[data-testid="message-bubble"]:visible')
    .filter({ hasText: 'Subagent reported' })
    .filter({ hasText: /PONG/ })).toBeVisible()
})
