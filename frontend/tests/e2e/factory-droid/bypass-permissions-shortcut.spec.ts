import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { droidTest } from '../droid-fixtures'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { editToolCall, readToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectNoControlBanner, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'
import { expectDroidNativeSettings } from './settingsUpdates'
import { nativeDroidCallId } from './toolResult'

droidTest.describe('Factory Droid settings', () => {
  droidTest('applies bypass to the native session before an edit', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    const filename = 'droid-bypass-note.txt'
    const path = join(askingDroidWorkspace.workingDir, filename)
    writeFileSync(path, 'before')
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await waitForSettingsIdle(page)
    await expectDroidNativeSettings({ page, leapmuxServer }, { interactionMode: 'auto', autonomyLevel: 'high' })
    await expectSettingsOptionChosen(page, 'permissionMode-auto-high')

    const start = await modelScript.queue(
      { toolCalls: [readToolCall(AgentProvider.DROID, 'bypass-read', path)] },
      { toolCalls: [editToolCall(AgentProvider.DROID, 'bypass-edit', { path, before: 'before', after: 'after' })] },
      { text: 'The edit completed.' },
    )
    await sendMessage(page, modelScript.prompt('Replace before with after in the note.'))
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)
    // The request after the read holds the read result, which states the bytes before the edit.
    expect((await modelScript.requestAt(start + 1)).body).toMatchObject({
      messages: expect.arrayContaining([expect.objectContaining({ role: 'tool', content: expect.stringContaining('before') })]),
    })
    await expectNoControlBanner(page)
    expect(readFileSync(path, 'utf8')).toBe('after')
  })
})

droidTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const native = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDroidWorkspace.workspaceId })
  const operation = (index: number) => {
    const path = join(askingDroidWorkspace.workingDir, `mid-bypass-${index}.txt`)
    const id = `mid-bypass-${index}`
    writeFileSync(path, 'before')
    return {
      toolCall: editToolCall(native.provider, id, { path, before: 'before', after: 'after' }),
      beforeDecision: () => { expect(readFileSync(path, 'utf8')).toBe('before') },
      nativeProof: (request: Parameters<typeof nativeToolResult>[0]) => {
        expect(readFileSync(path, 'utf8')).toBe('after')
        expect(nativeToolResult(request, nativeDroidCallId(request, 'Edit', id))).not.toMatch(/error|denied/i)
      },
    }
  }
  await exerciseMidTurnBypass(native, [operation(0), operation(1)])
})
