import { escapeRegExp } from '../../../src/lib/regexp'
import { codewhaleTest } from '../codewhale-fixtures'
import { codewhaleWorkflowToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, sendMessage, waitForSettingsHydrated } from '../helpers/ui'
import { expectOpaqueNativeWorkflowResult } from '../helpers/workflowGrouping'

codewhaleTest.describe('Codewhale workflow grouping', () => {
  codewhaleTest('shows one workflow row after its native child answers', async ({ native }) => {
    const { page, modelScript } = native
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')

    const goal = 'Probe one read-only child'
    await modelScript.rule({
      name: 'the workflow child answers',
      when: { user: 'Reply with CODEWHALE_WORKFLOW_CHILD' },
      respond: { text: 'CODEWHALE_WORKFLOW_CHILD' },
      once: true,
    })
    await modelScript.queue({
      toolCalls: [codewhaleWorkflowToolCall('run-workflow', goal, modelScript.prompt('Reply with CODEWHALE_WORKFLOW_CHILD.'))],
    })
    await modelScript.fallback({ text: 'The workflow finished.' })
    await sendMessage(page, modelScript.prompt('Run one read-only workflow child.'))
    await modelScript.waitForSteps()
    // No spec states the full heading text, so the pattern requires only the goal inside it.
    await expectOpaqueNativeWorkflowResult(native, { ruleNames: ['the workflow child answers'], heading: new RegExp(escapeRegExp(goal)) })
  })
})

codewhaleTest('keeps two actual native workflow units inside one opaque workflow row', async ({ native }) => {
  const { page, modelScript } = native
  await waitForSettingsHydrated(page)
  await applyPermissionPreset(page, 'bypass')
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
  await modelScript.queue({ toolCalls: [codewhaleWorkflowToolCall('two-workflow-units', 'Read two independent work units', 'Unused default.', children)] })
  await modelScript.fallback({ text: 'Both native workflow units completed.' })
  await sendMessage(page, modelScript.prompt('Run the two scripted native workflow assignments.'))
  await modelScript.waitForSteps()
  await expectOpaqueNativeWorkflowResult(native, { ruleNames: ['native-work-unit-0', 'native-work-unit-1'], heading: /Read two independent work units/ })
})
