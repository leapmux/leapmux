import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModeNotice } from '../helpers/nativeModeChange'
import { expect } from '@playwright/test'
import { CODEX_OPTION } from '../../../src/generated/contracts/codex-protocol'
import { codexTest } from '../codex-fixtures'
import { systemText } from '../helpers/mockModelScript'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption, exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { expectSettingsChip, waitForNativeSettingsHydrated } from '../helpers/ui'

function expectNativeCollaborationMode(request: MockModelRequestRecord, mode: 'plan' | 'default'): void {
  expect(request.protocol).toBe('openai-responses')
  const instructions = systemText(request.protocol, request.body)
  const headers = [
    ...instructions.matchAll(/<collaboration_mode>\s*(#[^\n]+)/g),
  ].map(match => match[1])
  expect(headers.at(-1)).toBe(mode === 'plan'
    ? '# Plan Mode (Conversational)'
    : '# Collaboration Mode: Default')
}

codexTest('applies and restores the selected native collaboration mode', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: CODEX_OPTION.CollaborationMode,
    value: 'plan',
    nativeProof: (request) => {
      expect(request.protocol).toBe('openai-responses')
      const instructions = nativeModelInstructionText(request)
      expect(instructions).toContain('<collaboration_mode>')
      expect(instructions).toMatch(/plan mode/i)
      expectNativeCollaborationMode(request, 'plan')
    },
  })
})

codexTest('returns from Plan to Default with one notice for each native mode change', async ({ native }) => {
  const { page } = native
  const groupId = CODEX_OPTION.CollaborationMode
  await waitForNativeSettingsHydrated(page)
  await expectSettingsOptionsOffered(page, groupId, ['default', 'plan'])
  await expectSettingsChip(page, 'Default')

  const notices: NativeModeNotice[] = []
  for (const change of [
    { value: 'plan', label: 'Plan Mode', previous: 'Default' },
    { value: 'default', label: 'Default', previous: 'Plan Mode' },
  ] as const) {
    const notice = await chooseNativeModeWithNotice(native, {
      groupId,
      ...change,
      previousLabel: change.previous,
      noticeLabel: 'Workflow',
    })
    notices.push(notice)
    const nativeProof = (request: MockModelRequestRecord) => {
      expectNativeCollaborationMode(request, change.value)
    }
    const request = await sendNativeAnswer(native, 'Reply once with the selected setting.', `The native ${change.label} mode answered.`)
    nativeProof(request)
    await exerciseRestoredNativeOption(native, { groupId, value: change.value, nativeProof })
    await expectSettingsChip(page, change.label)
    await notice.assertStored()
  }
  for (const notice of notices)
    await notice.assertStored()
})
