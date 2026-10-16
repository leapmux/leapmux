import { gooseTest } from '../goose-fixtures'
import { exerciseMidTurnBypass } from '../helpers/nativeMidTurnBypass'
import { createNativePermissionRemoval } from '../helpers/nativePermissionRemoval'
import { goosePermissionJudgmentToolCall } from '../helpers/providerToolCalls'
import { exerciseGoosePermissionRemoval, exerciseGooseShortcutSwitch } from './permissionScenario'

gooseTest('bypass-permissions-shortcut: permission shortcuts switch Smart Approve and Auto', async ({ native }) => {
  await exerciseGooseShortcutSwitch(native.page)
})

gooseTest('bypass-permissions-shortcut: smart mode asks before a removal and auto mode runs it', async ({ native }) => {
  await exerciseGoosePermissionRemoval(native)
})

gooseTest('approves a pending tool and the next tool when Bypass changes during a turn', async ({ native }) => {
  await native.modelScript.rule({ name: 'mid-bypass-goose-judge', when: { system: 'permission-safety classifier' }, respond: { toolCalls: [goosePermissionJudgmentToolCall('mid-bypass-judge', [])] } })
  const operations = await Promise.all([createNativePermissionRemoval(native), createNativePermissionRemoval(native)])
  await exerciseMidTurnBypass(native, operations)
})
