import type { TestInfo } from '@playwright/test'
import { describe, expect, it, vi } from 'vitest'
import { ampCatalogDiagnosticAttachment } from './nativeCatalogDiagnostic'

describe('ampCatalogDiagnosticAttachment', () => {
  it('passes the exact private file path and JSON type to the persistent report', async () => {
    const attach = vi.fn<TestInfo['attach']>().mockResolvedValue(undefined)
    const callback = ampCatalogDiagnosticAttachment({ attach })
    await callback({ path: '/private/run/amp-catalog-native.ownership.json' })
    expect(attach).toHaveBeenCalledExactlyOnceWith('amp-catalog-process-ownership', {
      path: '/private/run/amp-catalog-native.ownership.json',
      contentType: 'application/json',
    })
  })

  it('preserves the actual attachment failure for catalog cleanup to report', async () => {
    const failure = new Error('The persistent report could not copy the private evidence.')
    const attach = vi.fn<TestInfo['attach']>().mockRejectedValue(failure)
    await expect(ampCatalogDiagnosticAttachment({ attach })({ path: '/private/run/ownership.json' })).rejects.toBe(failure)
    expect(attach).toHaveBeenCalledTimes(1)
  })
})
