import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { codexExecToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expectNoNativeWorkflowGroups } from '../helpers/workflowGrouping'

codexTest('keeps real nested exec calls separate from a named workflow group', async ({ authenticatedCodexWorkspace, page, modelScript, leapmuxServer }) => {
  void authenticatedCodexWorkspace
  const source = 'const first = await tools.exec_command({cmd:"printf FLOWFIRST%s 42"}); text(first.output); const second = await tools.exec_command({cmd:"printf FLOWSECOND%s 77"}); text(second.output)'
  await modelScript.queue({ toolCalls: [codexExecToolCall('native-exec-sequence', source)] }, { text: 'The actual exec sequence ended.' })
  await sendMessage(page, modelScript.prompt('Run the two native exec calls in sequence.'))
  const status = await modelScript.waitForSteps(2)
  const result = nativeToolResult(status.requests.find(record => record.stepIndex === 1), 'native-exec-sequence')
  expect(result).toContain('FLOWFIRST42')
  expect(result).toContain('FLOWSECOND77')
  await waitForAgentIdle(page)
  await expectNoNativeWorkflowGroups({ page, leapmuxServer })
  await page.reload()
  await expectNoNativeWorkflowGroups({ page, leapmuxServer })
})
