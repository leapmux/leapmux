import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModeNotice } from '../helpers/nativeModeChange'
import { expect } from '@playwright/test'
import { FASTAGENT_MODE } from '../../../src/generated/contracts/fastagent-protocol'
import { fastAgentTest } from '../fastagent-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { currentNativeAgent, nativeModelInstructionText, nativeOptionGroup, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseNativeOption, exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { expectSettingsChip, expectSettingsOptionChosen, waitForNativeSettingsHydrated, waitForSettingsHydrated } from '../helpers/ui'
import { fastAgentModesTest } from './fixtures'
import { nativeContext } from './scenarios'

fastAgentTest.describe('Fast Agent settings', () => {
  fastAgentTest('the settings menu shows the agent mode', async ({ authenticatedFastAgentWorkspace, page }) => {
    void authenticatedFastAgentWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionsOffered(page, 'permissionMode', [FASTAGENT_MODE.Agent])
    await expectSettingsOptionChosen(page, `permissionMode-${FASTAGENT_MODE.Agent}`)
  })
})

fastAgentModesTest('returns from the writer to the reader and proves both native agents after reload', async ({ fastAgentModesWorkspace, page, modelScript }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer: fastAgentModesWorkspace.server,
    workspaceId: fastAgentModesWorkspace.workspaceId,
  })
  await waitForNativeSettingsHydrated(page)
  await expectSettingsOptionsOffered(page, 'permissionMode', ['reader', 'writer'])
  const group = nativeOptionGroup(await currentNativeAgent(context), 'permissionMode')
  expect(group?.label).toBe('Mode')
  const label = (value: string): string => {
    const option = group?.options.find(option => option.id === value)
    if (!option)
      throw new Error(`Fast Agent offers no ${value} mode.`)
    return option.name
  }
  await expectSettingsChip(page, label('reader'))
  const notices: NativeModeNotice[] = []
  for (const mode of [
    {
      value: 'writer',
      previous: 'reader',
      marker: 'NATIVE_FAST_AGENT_WRITER',
      absent: 'NATIVE_FAST_AGENT_READER',
    },
    {
      value: 'reader',
      previous: 'writer',
      marker: 'NATIVE_FAST_AGENT_READER',
      absent: 'NATIVE_FAST_AGENT_WRITER',
    },
  ]) {
    const notice = await chooseNativeModeWithNotice(context, {
      groupId: 'permissionMode',
      value: mode.value,
      label: label(mode.value),
      previousLabel: label(mode.previous),
      noticeLabel: 'Mode',
    })
    notices.push(notice)
    const nativeProof = (request: MockModelRequestRecord) => {
      const instruction = nativeModelInstructionText(request)
      expect(instruction).toContain(mode.marker)
      expect(instruction).not.toContain(mode.absent)
    }
    nativeProof(await sendNativeAnswer(context, `Reply through the ${mode.value} agent.`, `The ${mode.value} agent answered the mode sequence.`))
    await exerciseRestoredNativeOption(context, { groupId: 'permissionMode', value: mode.value, nativeProof })
    await expectSettingsChip(page, label(mode.value))
    await notice.assertStored()
  }
  for (const notice of notices)
    await notice.assertStored()
})

fastAgentModesTest('selects another configured native agent before and after reload', async ({ fastAgentModesWorkspace, page, modelScript }) => {
  const context = await nativeContext({
    page,
    modelScript,
    leapmuxServer: fastAgentModesWorkspace.server,
    workspaceId: fastAgentModesWorkspace.workspaceId,
  })
  expect(nativeOptionValue(await currentNativeAgent(context), 'permissionMode')).toBe('reader')
  const initial = await sendNativeAnswer(context, 'Reply through the initial configured reader agent.', 'The native reader agent answered.')
  expect(nativeModelInstructionText(initial)).toContain('NATIVE_FAST_AGENT_READER')
  expect(nativeModelInstructionText(initial)).not.toContain('NATIVE_FAST_AGENT_WRITER')
  await exerciseNativeOption(context, {
    groupId: 'permissionMode',
    value: 'writer',
    nativeProof: (request) => {
      const instruction = nativeModelInstructionText(request)
      expect(instruction).toContain('NATIVE_FAST_AGENT_WRITER')
      expect(instruction).not.toContain('NATIVE_FAST_AGENT_READER')
    },
  })
})

fastAgentTest.describe('Fast Agent settings apply', () => {
  fastAgentTest('keeps the chosen agent mode after reload', async ({ authenticatedFastAgentWorkspace, page }) => {
    void authenticatedFastAgentWorkspace
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, `permissionMode-${FASTAGENT_MODE.Agent}`)
    await page.reload()
    await waitForSettingsHydrated(page, 'permissionMode')
    await expectSettingsOptionChosen(page, `permissionMode-${FASTAGENT_MODE.Agent}`)
  })
})
