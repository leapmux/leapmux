import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { nativeUserStrings } from '../helpers/attachmentModelProbe'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { openChildTabFromRow } from '../helpers/subagentRegistry'
import { userBubbles } from '../helpers/ui'
import { waitForDeepseekHarnessChildReport } from './childReportCompletion'
import { nativeContext, runningChild } from './scenarios'

deepseekHarnessTest('sends a new prompt to the same continuable native child with its prior context', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  const child = await runningChild(context)
  await child.finish()
  expect(await openChildTabFromRow(page, child.row)).toBe(child.childId)
  const prompt = 'DEEPSEEKCHILDCONTINUE keep the earlier child file context.'
  const request = await sendNativeAnswer(context, prompt, 'The same native child accepted its next prompt.')
  const userContent = nativeUserStrings(request.body).join('\n')
  expect(userContent).toContain(prompt)
  expect(userContent).toContain('NATIVE_CHILD_FILE')
  await expect(userBubbles(page).filter({ hasText: prompt }).first()).toBeVisible()
  await waitForDeepseekHarnessChildReport(context, child.childId, child.parentId, 2)
})
