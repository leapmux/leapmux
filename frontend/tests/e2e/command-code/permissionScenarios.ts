import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, messageBubbles } from '../helpers/ui'

/** Verify native denial before bypass permits the same actual file write. */
export async function exerciseCommandCodePermissionLimit(context: ManagedNativeScenarioContext): Promise<void> {
  const agent = await currentNativeAgent(context)
  const path = join(agent.workingDir, 'native-permission-target.txt')
  const content = `native computed value: ${40 + 2}\n`
  const write = async (id: string) => {
    const turn = await runNativeToolTurn(context, {
      toolCalls: [writeToolCall(context.provider, id, { path, content })],
      prompt: 'Run the supplied native file write.',
      answer: 'The native write attempt completed.',
    })
    return nativeToolResult(turn.resultRequest, id)
  }
  await expectNoNativeControl(context, {
    testId: 'control-banner',
    relatedProof: async () => {
      const denial = await write('native-default-denial')
      expect(denial).toContain('requires permissions')
      expect(denial).toContain('--yolo')
      expect(existsSync(path)).toBe(false)
      await expect(messageBubbles(context.page).filter({ hasText: 'requires permissions' }).first()).toBeVisible()
    },
  })
  await applyPermissionPreset(context.page, 'bypass')
  // The tool turn allows any banner that appears, so the observation proves that Bypass runs the write with none.
  await expectNoNativeControl(context, {
    testId: 'control-banner',
    relatedProof: async () => {
      await write('native-bypass-write')
      expect(readFileSync(path, 'utf8')).toBe(content)
    },
  })
}
