import type { Locator } from '@playwright/test'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { copyNativeToolOutputPreview } from './nativeToolOutput'
import { openWorkspace, readAttachedWithArgument } from './ui'

export interface NativeToolOutputFilePathsOptions {
  context: Pick<ManagedNativeScenarioContext, 'page' | 'workspaceId'>
  callId: string
  previewText: string
  previewMarkers: readonly string[]
  paths: readonly string[]
  status: string
  prepareView?: (result: Locator) => Promise<void>
  /** Check the original native packet and its call/session owner on each pass. */
  workerProof: (reloaded: boolean) => Promise<void>
}

export interface NativeToolOutputFilePathsOperations {
  workerProof: (reloaded: boolean) => Promise<void>
  viewProof: () => Promise<void>
  copyProof: (previewText: string) => Promise<void>
  reload: () => Promise<void>
}

type ProofOptions = Pick<NativeToolOutputFilePathsOptions, 'callId' | 'previewText' | 'previewMarkers' | 'paths' | 'status'>

/** Check the native preview and paths before and after reload. Read no output file. */
export async function runNativeToolOutputFilePathsProof(
  options: ProofOptions,
  operations: NativeToolOutputFilePathsOperations,
): Promise<void> {
  if (!options.callId.trim() || !options.previewText || !options.status.trim())
    throw new Error('The native output path proof requires a call ID, native preview, and status.')
  if (options.previewMarkers.length === 0 || new Set(options.previewMarkers).size !== options.previewMarkers.length
    || options.previewMarkers.some(marker => !marker.trim() || !options.previewText.includes(marker))) {
    throw new Error('The native output path proof requires distinct markers from the original preview.')
  }
  if (new Set(options.paths).size !== options.paths.length || options.paths.some(path => !path.trim() || path.includes('\0')))
    throw new Error('The native output path proof requires distinct nonempty paths without NUL.')
  for (const reloaded of [false, true]) {
    if (reloaded)
      await operations.reload()
    await operations.workerProof(reloaded)
    await operations.viewProof()
    await operations.copyProof(options.previewText)
  }
}

/** Require paths before actual output blocks. Read markers only from those blocks. */
export function nativeOutputPathsPrecedePreview(matches: (SVGElement | HTMLElement)[], markers: readonly string[]): boolean | null {
  const result = matches.find(element => element.isConnected)
  if (!result)
    return null
  const pathBlocks = result.querySelectorAll('[data-testid="tool-output-file-paths"]')
  const paths = pathBlocks.length === 1 ? pathBlocks[0] : undefined
  if (!paths || markers.length === 0 || markers.some(marker => !marker.trim()))
    return false
  const previews = Array.from(result.querySelectorAll('[data-tool-output-preview]'))
    .filter(element => !paths.contains(element) && !element.contains(paths) && (element.textContent?.length ?? 0) > 0)
  if (previews.length === 0 || previews.some(preview => (paths.compareDocumentPosition(preview) & Node.DOCUMENT_POSITION_FOLLOWING) === 0))
    return false
  return markers.every(marker => previews.some(preview => preview.textContent?.includes(marker)))
}

/** Require the exact native result row. The added path text opens no file. */
export async function proveNativeToolOutputFilePaths(options: NativeToolOutputFilePathsOptions): Promise<void> {
  const escapedCallId = await options.context.page.evaluate(id => CSS.escape(id), options.callId)
  const result = options.context.page.locator(`[data-testid="message-bubble"][data-tool-call-id=${escapedCallId}][data-tool-row-role="result"]:visible`)
  await runNativeToolOutputFilePathsProof(options, {
    workerProof: options.workerProof,
    viewProof: async () => {
      await expect(result).toHaveCount(1)
      await expect(result).toHaveAttribute('data-tool-status', options.status)
      await options.prepareView?.(result)
      for (const marker of options.previewMarkers)
        await expect(result).toContainText(marker)
      const pathList = result.getByTestId('tool-output-file-paths')
      await expect(pathList).toHaveCount(options.paths.length === 0 ? 0 : 1)
      if (options.paths.length > 0) {
        expect(await pathList.textContent()).toBe(options.paths.map(path => `Output file:${path}`).join(''))
        await expect(pathList.locator('a, button, input, textarea')).toHaveCount(0)
        expect(await readAttachedWithArgument(result, 'native output property order', nativeOutputPathsPrecedePreview, options.previewMarkers)).toBe(true)
      }
    },
    copyProof: previewText => copyNativeToolOutputPreview(options.context.page, result, previewText),
    reload: async () => {
      await options.context.page.reload()
      await openWorkspace(options.context.page, options.context.workspaceId)
    },
  })
}
