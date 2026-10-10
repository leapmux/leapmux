import type { NativeAgentOpenOptions } from '../helpers/nativeAgentOpen'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { openNativeAgent } from '../helpers/nativeAgentOpen'
import { qwenWorkflowToolCall } from '../helpers/providerToolCalls'
import { sendMessage } from '../helpers/ui'
import { expectOpaqueNativeWorkflowResult } from '../helpers/workflowGrouping'
import { qwenTest } from '../qwen-fixtures'
import { qwenChildTurn } from './childScenario'
import { nativeContext } from './scenarios'

/** How the agent of each test opens: in YOLO mode, so its workflow children run with no permission request. */
const YOLO_OPEN: NativeAgentOpenOptions = { overrides: { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'yolo' } } }

qwenTest('keeps two actual native workflow units inside one opaque workflow row', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  await openNativeAgent(context, YOLO_OPEN)
  const workUnit = (label: string, task: string, answer: string) => ({ label, task, answer, prompt: modelScript.prompt(task) })
  const children = [
    workUnit('First native unit', 'Reply with FIRSTNATIVEWORKUNIT.', 'FIRSTNATIVEWORKUNIT'),
    workUnit('Second native unit', 'Reply with SECONDNATIVEWORKUNIT.', 'SECONDNATIVEWORKUNIT'),
  ] as const
  for (const [index, child] of children.entries()) {
    await modelScript.rule({
      name: `native-work-unit-${index}`,
      when: qwenChildTurn(child.task),
      respond: { text: child.answer },
      once: true,
    })
  }
  const [first, second] = children
  const start = await modelScript.queue({ toolCalls: [qwenWorkflowToolCall('two-workflow-units', [
    'export const meta = { name: "qwen-two-work-units", description: "Run two independent assignments." }',
    'phase("Two native assignments")',
    `const first = await agent(${JSON.stringify(first.prompt)}, { label: ${JSON.stringify(first.label)} })`,
    `const second = await agent(${JSON.stringify(second.prompt)}, { label: ${JSON.stringify(second.label)} })`,
    'return { first, second }',
  ].join('\n'))] })
  await modelScript.fallback({ text: 'Both native workflow units completed.' })
  await sendMessage(page, modelScript.prompt('Run the two scripted native workflow assignments.'))
  await modelScript.waitForSteps(start + 1)
  await expectOpaqueNativeWorkflowResult(context, { ruleNames: ['native-work-unit-0', 'native-work-unit-1'], heading: 'Workflow' })
})
