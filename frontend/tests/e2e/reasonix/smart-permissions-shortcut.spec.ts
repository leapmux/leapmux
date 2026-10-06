import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixSessionSettings } from './settingsScenario'

// Reasonix 1.38 asks to leave Plan mode after each Plan-mode answer, also under the Bypass preset. The settings scenario
// answers each of those requests, so no request stays open across the Bypass change and the reload.
reasonixTest('smart-permissions-shortcut: applies Reasonix session settings and preserves them after reload', async ({ native }) => {
  await exerciseReasonixSessionSettings(native)
})

reasonixTest('proves the native smart-permissions-shortcut limit after a real sidebar operation', async ({ native }) => {
  const relatedProof = () => exerciseRelatedTodo(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof })
})
