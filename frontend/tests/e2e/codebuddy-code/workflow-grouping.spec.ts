import { globSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from '../codebuddy-fixtures'
import { codebuddyFindWorkflowToolCall, codebuddyWorkflowToolCall, readToolCall } from '../helpers/providerToolCalls'
import { expandBackgroundTasksSection, expectRowBecomesFinal, openChildTabFromRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, tabById, userBubbles } from '../helpers/ui'
import { workflowGroupHeading, workflowRowsShareGroup } from '../helpers/workflowGrouping'

codebuddyTest.describe('CodeBuddy Code workflow grouping', () => {
  codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

  codebuddyTest('groups a native Workflow run with its child agent', async ({ codebuddyWorkspace, leapmuxServer, page, modelScript }) => {
    const markerPath = join(codebuddyWorkspace.workingDir, 'workflow-child-marker.txt')
    writeFileSync(markerPath, 'WORKFLOW_CHILD_FILE_MARKER\n')
    const childPrompt = modelScript.prompt(`Read ${markerPath}, then reply with WORKFLOW_CHILD.`)
    const script = [
      'export const meta = { name: "leapmux-workflow", description: "Ask one child." }',
      'phase("Probe")',
      `const answer = await agent(${JSON.stringify(childPrompt)}, { label: "Probe child" })`,
      'return answer',
    ].join('\n')
    await modelScript.rule({
      name: 'workflow child reads its marker',
      when: { user: 'Read .*workflow-child-marker.txt' },
      respond: { toolCalls: [readToolCall(AgentProvider.CODEBUDDY, 'child-workflow-read', markerPath)] },
      once: true,
    }, {
      name: 'workflow child answers after its tool result',
      when: { body: 'WORKFLOW_CHILD_FILE_MARKER' },
      respond: { text: 'WORKFLOW_CHILD' },
      once: true,
    })
    await modelScript.queue(
      { toolCalls: [codebuddyFindWorkflowToolCall('find-workflow')] },
      { toolCalls: [codebuddyWorkflowToolCall('run-workflow', script)] },
    )
    await modelScript.fallback({ text: 'Workflow dispatched.' })

    const parentTabId = await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().getAttribute('data-tab-id') ?? ''
    expect(parentTabId).not.toBe('')
    await sendMessage(page, modelScript.prompt('Use a workflow to ask one child for a reply.'))
    await modelScript.waitForSteps()
    await expandBackgroundTasksSection(page)
    const workflow = page.locator('[data-testid="bg-task-row"]:visible[data-kind="workflow"]').first()
    const child = page.locator('[data-testid="bg-task-row"]:visible[data-kind="subagent"]').filter({ hasText: 'Probe child' }).first()
    await expect(workflow).toBeVisible()
    await expect(child).toBeVisible()
    const groupHeading = await workflowGroupHeading(workflow)
    expect(groupHeading).not.toBe('')
    await expect.poll(() => workflowGroupHeading(child)).toBe(groupHeading)
    await expect.poll(() => workflowRowsShareGroup(workflow, child)).toBe(true)

    await expect.poll(async () => (await modelScript.status()).ruleMatches['workflow child reads its marker'] ?? 0).toBe(1)
    await expect.poll(async () => (await modelScript.status()).ruleMatches['workflow child answers after its tool result'] ?? 0).toBe(1)
    await expectRowBecomesFinal(page, workflow)
    const configDir = leapmuxServer.agentEnv.CODEBUDDY_CONFIG_DIR
    if (!configDir)
      throw new Error('the isolated CodeBuddy configuration path is absent')
    const archives = globSync('projects/*/*/subagents/agent-*.jsonl', { cwd: configDir })
    const archive = archives.find(path => readFileSync(join(configDir, path), 'utf8').includes('workflow-child-marker.txt'))
    if (!archive)
      throw new Error('the native child archive is absent')
    const nativeTools = readFileSync(join(configDir, archive), 'utf8').trim().split('\n').map((line) => {
      const row = JSON.parse(line) as Record<string, unknown>
      return {
        type: row.type,
        callID: row.call_id,
        nativeCallID: row.callId,
        name: row.name,
        status: row.status,
      }
    }).filter(row => row.type === 'function_call' || row.type === 'function_call_result' || row.type === 'function_call_output')
    expect(nativeTools).toEqual([
      { type: 'function_call', callID: undefined, nativeCallID: 'child-workflow-read', name: 'Read', status: undefined },
      { type: 'function_call_result', callID: undefined, nativeCallID: 'child-workflow-read', name: 'Read', status: 'completed' },
    ])
    await expect.poll(async () => await child.getAttribute('data-child-agent-id') ?? '').not.toBe('')
    await openChildTabFromRow(page, child)
    await expect(userBubbles(page).filter({ hasText: 'Read' }).filter({ hasText: 'workflow-child-marker.txt' }).first()).toBeVisible()
    const childToolRequest = page.locator('[data-testid="message-bubble"]:visible[data-tool-row-role="request"][data-tool-call-id="child-workflow-read"]')
    const childToolResult = page.locator('[data-testid="message-bubble"]:visible[data-tool-row-role="result"][data-tool-call-id="child-workflow-read"]')
    await expect(childToolRequest).toContainText('workflow-child-marker.txt')
    await expect(childToolResult).toContainText('WORKFLOW_CHILD_FILE_MARKER')
    const requestSeq = await childToolRequest.getAttribute('data-message-seq')
    const resultSeq = await childToolResult.getAttribute('data-message-seq')
    expect(requestSeq).toMatch(/^\d+$/)
    expect(resultSeq).toMatch(/^\d+$/)
    if (!requestSeq || !resultSeq)
      throw new Error('the child tool rows need message sequences')
    expect(BigInt(requestSeq)).toBeLessThan(BigInt(resultSeq))
    await expect(assistantBubbles(page).filter({ hasText: 'WORKFLOW_CHILD' }).first()).toBeVisible()
    await tabById(page, parentTabId).click()
    await expect(assistantBubbles(page).filter({ hasText: 'WORKFLOW_CHILD' })).toHaveCount(0)
    await expect(page.locator('[data-testid="message-bubble"]:visible[data-tool-call-id="child-workflow-read"]')).toHaveCount(0)
  })
})
