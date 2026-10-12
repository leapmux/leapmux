import type { TestInfo } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import type { AgentWorkspace } from './helpers/workspace'
import type { CliSkipFixture, NativeFixture } from './provider-fixture-factory'
import { writeFile } from 'node:fs/promises'
import { test as base } from './fixtures'
import { missingBinaryReason } from './helpers/binaryOnPath'
import { withCleanup } from './helpers/cleanup'
import { authenticatedAgentWorkspace } from './helpers/workspace'
import { MUSE_AGENT, nativeContext } from './muse-code/scenarios'
import { cliSkipFixture } from './provider-fixture-factory'

export const MUSE_E2E_SKIP_REASON: string | null = missingBinaryReason('muse', 'Muse Code E2E requires the native muse executable.')

/** Retain the actual model status before the script's inherited cleanup. */
export async function withMuseModelReceipt<T>(
  script: Pick<ModelScript, 'status'>,
  report: Pick<TestInfo, 'outputPath' | 'attach'>,
  operation: () => Promise<T>,
): Promise<T> {
  return withCleanup(operation, async () => {
    const status = await script.status()
    const path = report.outputPath('muse-model-script.json')
    await writeFile(path, JSON.stringify(status), 'utf8')
    await report.attach('muse-model-script', { path, contentType: 'application/json' })
  })
}

export const museTest = base.extend<CliSkipFixture & NativeFixture & {
  authenticatedMuseWorkspace: AgentWorkspace
  museModelReceipt: void
}>({
  cliSkip: cliSkipFixture(MUSE_E2E_SKIP_REASON),
  authenticatedMuseWorkspace: authenticatedAgentWorkspace({ ...MUSE_AGENT, openOptions: { optionValues: { permissionMode: 'allowAll' } } }),
  native: async ({ page, modelScript, leapmuxServer, authenticatedMuseWorkspace }, use) => {
    await use(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedMuseWorkspace.workspaceId }))
  },
  museModelReceipt: [async ({ modelScript }, use, report) => {
    await withMuseModelReceipt(modelScript, report, use)
  }, { auto: true }],
})
