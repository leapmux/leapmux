import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { qwenWorkflowToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, openWorkspace, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectOpaqueNativeWorkflowResult } from '../helpers/workflowGrouping'
import { openProviderAgent } from '../helpers/workspace'
import { QWEN_AGENT, qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code workflow grouping', () => {
  qwenTest('shows one workflow row after its native child answers', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)

    const childPrompt = modelScript.prompt('Reply with QWEN_WORKFLOW_CHILD.')
    const script = [
      'export const meta = { name: "qwen-e2e-workflow", description: "Ask one child." }',
      'phase("Probe")',
      `const answer = await agent(${JSON.stringify(childPrompt)}, { label: "Probe child" })`,
      'return answer',
    ].join('\n')
    await modelScript.rule({
      name: 'the workflow child answers',
      when: { user: 'Reply with QWEN_WORKFLOW_CHILD' },
      respond: { text: 'QWEN_WORKFLOW_CHILD' },
      once: true,
    })
    await modelScript.queue(
      { toolCalls: [qwenWorkflowToolCall('run-workflow', script)] },
      { text: 'The workflow finished.' },
    )
    await sendMessage(page, modelScript.prompt('Run one workflow child and report completion.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The workflow finished.' }).first()).toBeVisible()
    await expectOpaqueNativeWorkflowResult(
      { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE },
      { ruleNames: ['the workflow child answers'], heading: 'Workflow' },
    )
  })
})

qwenTest('keeps two actual native workflow units inside one opaque workflow row', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, QWEN_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  const children = [
    { label: 'First native unit', prompt: modelScript.prompt('Reply with FIRSTNATIVEWORKUNIT.') },
    { label: 'Second native unit', prompt: modelScript.prompt('Reply with SECONDNATIVEWORKUNIT.') },
  ]
  for (const [index, child] of children.entries()) {
    await modelScript.rule({
      name: `native-work-unit-${index}`,
      when: { user: child.prompt },
      respond: { text: index === 0 ? 'FIRSTNATIVEWORKUNIT' : 'SECONDNATIVEWORKUNIT' },
      once: true,
    })
  }
  await modelScript.queue({ toolCalls: [qwenWorkflowToolCall('two-workflow-units', [
    'export const meta = { name: "qwen-two-work-units", description: "Run two independent assignments." }',
    'phase("Two native assignments")',
    `const first = await agent(${JSON.stringify(children[0]!.prompt)}, { label: "First native unit" })`,
    `const second = await agent(${JSON.stringify(children[1]!.prompt)}, { label: "Second native unit" })`,
    'return { first, second }',
  ].join('\n'))] })
  await modelScript.fallback({ text: 'Both native workflow units completed.' })
  await sendMessage(page, modelScript.prompt('Run the two scripted native workflow assignments.'))
  await modelScript.waitForSteps()
  await expectOpaqueNativeWorkflowResult(context, { ruleNames: ['native-work-unit-0', 'native-work-unit-1'], heading: 'Workflow' })
})
