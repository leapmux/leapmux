import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { editToolCall } from '../helpers/providerToolCalls'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { nativeContext } from './scenarios'
import { nativeDroidCallId } from './toolResult'

droidTest('resolves an actual native permission without a multiline editor request', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDroidWorkspace.workspaceId })
  const agent = await currentNativeAgent(context)
  const file = join(agent.workingDir, 'editor-proof.txt')
  writeFileSync(file, 'EDITOR_BEFORE')
  await expectNoNativeEditorRequest(context, { relatedControl: () => exerciseNativePermissionDecision(context, {
    decision: 'allow',
    toolCall: editToolCall(context.provider, 'editor-permission', { path: file, before: 'EDITOR_BEFORE', after: 'EDITOR_AFTER' }),
    beforeDecision: async (banner) => {
      await expect(banner).toBeVisible()
      expect(readFileSync(file, 'utf8')).toBe('EDITOR_BEFORE')
    },
    nativeProof: async (request) => {
      expect(readFileSync(file, 'utf8')).toBe('EDITOR_AFTER')
      const callId = nativeDroidCallId(request, 'Edit', 'editor-permission')
      expect(nativeToolResult(request, callId)).not.toMatch(/denied|failed/i)
    },
  }) })
})
