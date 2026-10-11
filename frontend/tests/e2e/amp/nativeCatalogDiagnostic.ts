import type { TestInfo } from '@playwright/test'

/** Copy private native ownership evidence into the persistent test report. */
export function ampCatalogDiagnosticAttachment(testInfo: Pick<TestInfo, 'attach'>): (diagnostic: { path: string }) => Promise<void> {
  return ({ path }) => testInfo.attach('amp-catalog-process-ownership', { path, contentType: 'application/json' })
}
