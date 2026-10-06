import { expect } from '@playwright/test'
import { ampExtractControl } from '../../../src/components/chat/providers/amp/extractControl'
import { ampTest } from '../amp-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, nativeToolOutcome } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'

ampTest('disables the actual native question tool while real permission controls still work', async ({ native }, testInfo) => {
  const { page } = native
  await chooseSettingsOption(page, 'permissionMode-ask')
  await waitForSettingsIdle(page)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page, 'permissionMode')
    }
    const catalog = await readAmpExecutorCatalog(native, { onOwnershipDiagnostic: ampCatalogDiagnosticAttachment(testInfo) })
    expect(catalog.settings['amp.tools.disable']).toContain('ask_user_choice')
    expect(catalog.tools).toContain('shell_command')
    expect(catalog.tools).not.toContain('ask_user_choice')
    const agent = await currentNativeAgent(native)
    const watch = await watchNativeControls(native.leapmuxServer, agent.id)
    const callId = `amp-question-limit-${reload}`
    await withCleanup(() => expectNoNativeControl(native, { testId: 'control-question-group', relatedProof: () => expectNoNativeControl(native, { testId: 'elicitation-form', relatedProof: () => exerciseNativePermissionDecision(native, {
      toolCall: bashToolCall(native.provider, callId, 'printf "AMPQUESTIONLIMIT%s\\n" "$((40 + 2))"'),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('AMPQUESTIONLIMIT')
        await expect.poll(() => watch.controls().some(frame => ampExtractControl({ payload: frame.payload })?.kind === 'permission')).toBe(true)
      },
      nativeProof: async (request) => {
        expect((await nativeToolOutcome(native, request, callId)).text).toContain('AMPQUESTIONLIMIT42')
        const frames = watch.controls()
        expect(frames.length).toBeGreaterThan(0)
        expect(frames.every(frame => ampExtractControl({ payload: frame.payload })?.kind === 'permission')).toBe(true)
      },
    }) }) }), async () => watch.cancel())
  }
})
