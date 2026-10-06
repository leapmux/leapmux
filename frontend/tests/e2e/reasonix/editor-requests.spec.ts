import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { answerControl, chooseSettingsOption, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('resolves an actual native control without exposing a multiline editor request', async ({ native, authenticatedReasonixWorkspace }) => {
  const { page, modelScript } = native
  const directory = authenticatedReasonixWorkspace.workingDir
  if (!directory)
    throw new Error('The native editor control proof requires a working directory.')
  const path = join(directory, 'native-editor-control.txt')
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  await expectNoNativeEditorRequest(native, {
    relatedProof: async () => {
      const start = await modelScript.queue({ toolCalls: [writeToolCall(native.provider, 'native-editor-limit-write', { path, content: 'NATIVE_EDITOR_CONTROL_BYTES\n' })] }, { text: 'The native approval proof ended.' })
      await sendMessage(page, modelScript.prompt('Run the scripted native file write.'))
      await modelScript.waitForSteps(start + 1)
      const banner = await waitForControlBanner(page)
      await expect(banner).toContainText('native-editor-control.txt')
      await answerControl(page, 'allow')
      await modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(page)
      expect(readFileSync(path, 'utf8')).toBe('NATIVE_EDITOR_CONTROL_BYTES\n')
      expect(nativeToolResult(await modelScript.requestAt(start + 1), 'native-editor-limit-write')).toMatch(/native-editor-control|success|write/i)
    },
  })
})
