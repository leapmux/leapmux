import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { COPILOT_PERMISSION_MODE } from '../../../src/generated/contracts/copilot-protocol'
import { exerciseNativeToolWrite } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { applyPermissionPreset } from '../helpers/ui'

/** Prove the selected native Copilot preset with actual file bytes and a matched tool result. */
export async function exerciseCopilotPermissionPreset(context: ManagedNativeScenarioContext, preset: 'smart' | 'bypass'): Promise<void> {
  await applyPermissionPreset(context.page, preset)
  const agent = await currentNativeAgent(context)
  expect(agent.optionGroups.find(group => group.id === 'permissionMode')?.currentValue)
    .toBe(preset === 'smart' ? COPILOT_PERMISSION_MODE.Assisted : COPILOT_PERMISSION_MODE.AllowAll)
  await exerciseNativeToolWrite(context, { permission: preset === 'smart' ? 'native' : 'absent' })
}
