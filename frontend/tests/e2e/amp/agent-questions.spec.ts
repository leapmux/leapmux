import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { ampExtractControl } from '../../../src/components/chat/providers/amp/extractControl'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AMP_E2E_SKIP_REASON, ampTest } from '../amp-fixtures'
import { ampToolResultReader } from '../helpers/ampToolResult'
import { withCleanup } from '../helpers/cleanup'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { bashToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { readAmpExecutorCatalog } from './nativeCatalog'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest('disables the actual native question tool while real permission controls still work', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }, testInfo) => {
  const context: ManagedNativeScenarioContext = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  context.readToolResult = ampToolResultReader(context)
  await chooseSettingsOption(page, 'permissionMode-ask')
  await waitForSettingsIdle(page)
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
      await waitForSettingsHydrated(page, 'permissionMode')
    }
    const catalog = await readAmpExecutorCatalog(context, { onOwnershipDiagnostic: ampCatalogDiagnosticAttachment(testInfo) })
    expect(catalog.settings['amp.tools.disable']).toContain('ask_user_choice')
    expect(catalog.tools).toContain('shell_command')
    expect(catalog.tools).not.toContain('ask_user_choice')
    const agent = await currentNativeAgent(context)
    const watch = await watchNativeControls(leapmuxServer, agent.id)
    await withCleanup(() => expectNoNativeControl(context, { testId: 'control-question-group', relatedControl: () => expectNoNativeControl(context, { testId: 'elicitation-form', relatedControl: () => exerciseNativePermissionDecision(context, {
      toolCall: bashToolCall(AgentProvider.AMP, `amp-question-limit-${reload}`, 'printf "AMPQUESTIONLIMIT%s\\n" "$((40 + 2))"'),
      decision: 'allow',
      beforeDecision: async (banner) => {
        await expect(banner).toContainText('AMPQUESTIONLIMIT')
        await expect.poll(() => watch.controls().some(frame => ampExtractControl({ payload: frame.payload })?.kind === 'permission')).toBe(true)
      },
      nativeProof: async (request) => {
        expect((await ampToolResultReader(context)(request, `amp-question-limit-${reload}`)).text).toContain('AMPQUESTIONLIMIT42')
        const frames = watch.controls()
        expect(frames.length).toBeGreaterThan(0)
        expect(frames.every(frame => ampExtractControl({ payload: frame.payload })?.kind === 'permission')).toBe(true)
      },
    }) }) }), async () => watch.cancel())
  }
})
