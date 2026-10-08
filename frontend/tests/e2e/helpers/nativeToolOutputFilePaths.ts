import type { Locator, TestInfo } from '@playwright/test'
import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativeMessageSnapshot } from './nativeMessages'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { NativeToolOutput } from './nativeToolOutput'
import type { NativeToolOutputCapture } from './nativeToolOutputScenario'
import { expect } from '@playwright/test'
import { assertPrivateNativePath } from './nativeCredentialIsolation'
import { readNativeMessageSnapshot } from './nativeMessages'
import { expandNativeResultView } from './nativeResultView'
import { currentNativeAgent } from './nativeScenario'
import { copyNativeToolOutputPreview } from './nativeToolOutput'
import { nativeToolRowId } from './nativeToolRowId'
import { getGlobalState } from './server'
import { openWorkspace, readAttachedWithArgument, toolCallRow } from './ui'

export interface NativeToolOutputFilePathsOptions {
  context: Pick<ManagedNativeScenarioContext, 'page' | 'workspaceId' | 'resolveToolRowId'>
  callId: string
  previewText: string
  previewMarkers: readonly string[]
  /**
   * Text that the original preview does not hold, such as the omitted middle line of a computed output.
   * The result row must not show it, before and after the reload.
   */
  absentMarkers?: readonly string[]
  paths: readonly string[]
  status: string
  /**
   * Bring the result row into the rendered chat before the view proof counts it.
   * The chat renders only rows near its scroll position. A row far above the end has no element until the chat scrolls to it.
   */
  revealView?: () => Promise<void>
  /** Prepare the counted result row for the marker checks, for example expand it. */
  prepareView?: (result: Locator) => Promise<void>
  /**
   * Prove what a provider's result row shows beyond the paths and the preview, such as images.
   * It runs after the shared row checks on each pass.
   */
  rowProof?: (result: Locator) => Promise<void>
  /** Check the original native packet and its call/session owner on each pass. */
  workerProof: (reloaded: boolean) => Promise<void>
}

export interface NativeToolOutputFilePathsOperations {
  workerProof: (reloaded: boolean) => Promise<void>
  viewProof: () => Promise<void>
  copyProof: (previewText: string) => Promise<void>
  reload: () => Promise<void>
}

type ProofOptions = Pick<NativeToolOutputFilePathsOptions, 'callId' | 'previewText' | 'previewMarkers' | 'absentMarkers' | 'paths' | 'status'>

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
  // Each preview marker comes from the preview. A marker that the preview holds cannot be absent.
  // This check also rejects an absent marker inside a preview marker.
  const absentMarkers = options.absentMarkers ?? []
  if (new Set(absentMarkers).size !== absentMarkers.length
    || absentMarkers.some(marker => !marker.trim() || options.previewText.includes(marker))) {
    throw new Error('The native output path proof requires distinct absent markers that the original preview does not hold.')
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

/**
 * Require one path block before every nonempty marked output block. Read markers only from marked output blocks.
 * Return null when no match is attached, so that the caller reads again.
 * Reject a marked element that wraps the path block or sits inside it. That element can hide output before the paths.
 */
export function nativeOutputPathsPrecedePreview(matches: (SVGElement | HTMLElement)[], markers: readonly string[]): boolean | null {
  const result = matches.find(element => element.isConnected)
  if (!result)
    return null
  const pathBlocks = result.querySelectorAll('[data-testid="tool-output-file-paths"]')
  const paths = pathBlocks.length === 1 ? pathBlocks[0] : undefined
  if (!paths || markers.length === 0 || markers.some(marker => !marker.trim()))
    return false
  const marked = Array.from(result.querySelectorAll('[data-tool-output-preview]'))
  if (marked.some(element => element.contains(paths) || paths.contains(element)))
    return false
  const previews = marked.filter(element => (element.textContent?.length ?? 0) > 0)
  if (previews.length === 0 || previews.some(preview => (paths.compareDocumentPosition(preview) & Node.DOCUMENT_POSITION_FOLLOWING) === 0))
    return false
  return markers.every(marker => previews.some(preview => preview.textContent?.includes(marker)))
}

/** Require the exact native result row. The added path text opens no file. */
export async function proveNativeToolOutputFilePaths(options: NativeToolOutputFilePathsOptions): Promise<void> {
  const result = toolCallRow(options.context.page, await nativeToolRowId(options.context, options.callId))
  await runNativeToolOutputFilePathsProof(options, {
    workerProof: options.workerProof,
    viewProof: async () => {
      await options.revealView?.()
      await expect(result).toHaveCount(1)
      await expect(result).toHaveAttribute('data-tool-status', options.status)
      await options.prepareView?.(result)
      // Wait for each marker inside marked output. Argument text can hold the same marker.
      const outputs = result.locator('[data-tool-output-preview]')
      for (const marker of options.previewMarkers)
        await expect(outputs.filter({ hasText: marker })).not.toHaveCount(0)
      // The whole row must omit an absent marker, its arguments included.
      for (const marker of options.absentMarkers ?? [])
        await expect(result, `the result row does not show ${marker}`).not.toContainText(marker)
      const pathList = result.getByTestId('tool-output-file-paths')
      await expect(pathList).toHaveCount(options.paths.length === 0 ? 0 : 1)
      if (options.paths.length > 0) {
        expect(await pathList.textContent()).toBe(options.paths.map(path => `Output file:${path}`).join(''))
        await expect(pathList.locator('a, button, input, textarea')).toHaveCount(0)
        expect(await readAttachedWithArgument(result, 'native output property order', nativeOutputPathsPrecedePreview, options.previewMarkers)).toBe(true)
      }
      await options.rowProof?.(result)
    },
    copyProof: previewText => copyNativeToolOutputPreview(options.context.page, result, previewText),
    reload: async () => {
      await options.context.page.reload()
      await openWorkspace(options.context.page, options.context.workspaceId)
    },
  })
}

/**
 * One native result that declares output file paths, as the output path reader of a provider returns it.
 * A reader can return more fields. The receipt proof compares them too.
 */
export interface NativeOutputReceipt {
  /** The output file paths that the native result declares. */
  paths: readonly string[]
  /** The text that the native result shows, and that Copy copies. */
  previewText: string
  /** The original native frame of the Worker row. */
  frame: Record<string, unknown>
  /** The original bytes of the Worker row. */
  content: Uint8Array
}

/** Return the first line and the last line of the computed output that `previewText` holds, in that order. */
export function presentPreviewMarkers(previewText: string, output: Pick<NativeToolOutput, 'firstMarker' | 'lastMarker'>): string[] {
  return [output.firstMarker, output.lastMarker].filter(marker => previewText.includes(marker))
}

/** The preview markers that a receipt proof states: a list, or a function that selects them from the receipt. */
export type StatedPreviewMarkers<R> = readonly string[] | ((receipt: R) => readonly string[])

/** The markers of a preview that one receipt proof uses. */
export interface NativeOutputReceiptMarkers<R> {
  /** The markers that the preview must hold. See `checkNativeOutputReceipt` for the default. */
  previewMarkers?: StatedPreviewMarkers<R>
  /**
   * The line of the computed output that the truncated preview must not hold. The omitted middle line by default.
   * A provider whose preview can be any window of the output states another line here, with its reason.
   */
  absentMarker?: string
  /**
   * True when the tool call passes the complete output as one of its arguments, as an MCP echo call does. The result
   * row draws the arguments of its call, so the row holds every line of the output. The preview must still omit the
   * absent line, but the row cannot omit it.
   */
  argumentsHoldOutput?: boolean
}

/** What `checkNativeOutputReceipt` returns for one receipt. */
export interface CheckedNativeOutputReceipt {
  /** The one path that the receipt declares. */
  path: string
  /** The markers that the preview holds. */
  previewMarkers: string[]
  /** The line that the preview does not hold. */
  absentMarker: string
  /** The lines that the result row must not show: the absent line, or none when the arguments hold the output. */
  rowAbsentMarkers: string[]
}

/**
 * Check one receipt against the computed output. Return these fields:
 *
 * - Its one declared path.
 * - The markers of its preview.
 * - The line that its preview must not hold.
 * - The lines that its result row must not show.
 *
 * The receipt must declare exactly one path. Its preview must not hold the absent line, which is the omitted middle line by default.
 * Without `previewMarkers`, use the first and last output lines that the preview holds. The preview must hold at least one of them.
 */
export function checkNativeOutputReceipt<R extends Pick<NativeOutputReceipt, 'paths' | 'previewText'>>(
  receipt: R,
  output: Pick<NativeToolOutput, 'firstMarker' | 'omittedMarker' | 'lastMarker'>,
  markers: NativeOutputReceiptMarkers<R> = {},
): CheckedNativeOutputReceipt {
  const path = receipt.paths.length === 1 ? receipt.paths[0] : undefined
  if (path === undefined)
    throw new Error(`The native output receipt requires exactly one declared path, not ${receipt.paths.length}.`)
  const absentMarker = markers.absentMarker ?? output.omittedMarker
  if (!absentMarker.trim())
    throw new Error('The native output receipt proof requires a nonempty absent line.')
  if (receipt.previewText.includes(absentMarker))
    throw new Error(`The native output preview holds ${absentMarker === output.omittedMarker ? 'the omitted middle line' : 'the absent line'} of the computed output.`)
  const rowAbsentMarkers = markers.argumentsHoldOutput === true ? [] : [absentMarker]
  if (markers.previewMarkers !== undefined) {
    const stated = typeof markers.previewMarkers === 'function' ? markers.previewMarkers(receipt) : markers.previewMarkers
    if (stated.length === 0)
      throw new Error('The native output receipt proof requires at least one preview marker.')
    return { path, previewMarkers: [...stated], absentMarker, rowAbsentMarkers }
  }
  const present = presentPreviewMarkers(receipt.previewText, output)
  if (present.length === 0)
    throw new Error('The native output preview holds neither the first line nor the last line of the computed output.')
  return { path, previewMarkers: present, absentMarker, rowAbsentMarkers }
}

/**
 * Require that the selected agent and its native session remain unchanged. Require that `read` returns the same Worker record.
 * `read` reads a fresh Worker snapshot on each call, so a record that changed after `expected` fails the check.
 */
export async function expectUnchangedNativeRecord<T>(
  context: Pick<ManagedNativeScenarioContext, 'page' | 'leapmuxServer'>,
  agent: Pick<AgentInfo, 'id' | 'agentSessionId'>,
  read: (snapshot: NativeMessageSnapshot) => T | Promise<T>,
  expected: T,
): Promise<void> {
  const current = await currentNativeAgent(context)
  expect(current.id, 'the selected agent is the agent that ran the tool').toBe(agent.id)
  expect(current.agentSessionId, 'the agent keeps its native session').toBe(agent.agentSessionId)
  const snapshot = await readNativeMessageSnapshot(context, agent.id)
  expect(snapshot.agentSessionId, 'the Worker snapshot belongs to the native session').toBe(agent.agentSessionId)
  // Playwright selects its matchers from the value's type. A type parameter leaves that choice open.
  // Convert the value to `unknown` to select the generic matchers.
  const record: unknown = await read(snapshot)
  expect(record, 'the Worker record of the native result stays the same').toEqual(expected)
}

/** Optional facts of one receipt proof. */
export interface NativeOutputReceiptOptions<R extends NativeOutputReceipt> extends NativeOutputReceiptMarkers<R> {
  /** The directory that must hold the declared path. The end-to-end (E2E) run directory by default. */
  privateRoot?: string
  /** Prepare the result row for the view proof. `expandNativeResultView` by default. */
  prepareView?: (result: Locator) => Promise<void>
  /** A provider proof of the first receipt. It runs after the receipt checks and before the view proof. */
  extraProof?: (receipt: R) => void | Promise<void>
}

/**
 * Prove one declared private output path and its native preview before and after reload.
 * Require that the Worker record remains unchanged.
 *
 * `read` is the provider's output path reader. Read the receipt from the captured snapshot and attach it.
 * Require one private path and a preview without the absent line, which is the omitted middle line by default.
 * The result row must hold the preview markers. It must omit the absent line unless the call's arguments hold the output (`argumentsHoldOutput`).
 * On each pass, read the receipt again through `read`. Require that the complete receipt remains unchanged.
 */
export async function proveNativeOutputReceipt<R extends NativeOutputReceipt>(
  capture: Pick<NativeToolOutputCapture, 'context' | 'agent' | 'snapshot' | 'nativeCallId' | 'output'>,
  testInfo: Pick<TestInfo, 'attach'>,
  read: (snapshot: NativeMessageSnapshot, callId: string) => R,
  options: NativeOutputReceiptOptions<R> = {},
): Promise<void> {
  const callId = capture.nativeCallId
  const receipt = read(capture.snapshot, callId)
  // Attach the receipt before its checks, so a failed check keeps the evidence.
  await testInfo.attach('native-output-path-receipt', {
    body: JSON.stringify({ agentId: capture.agent.id, sessionId: capture.agent.agentSessionId, callId, paths: receipt.paths, previewText: receipt.previewText, frame: receipt.frame }),
    contentType: 'application/json',
  })
  const { path, previewMarkers, rowAbsentMarkers } = checkNativeOutputReceipt(receipt, capture.output, options)
  assertPrivateNativePath(path, options.privateRoot ?? getGlobalState().tmpDir)
  await options.extraProof?.(receipt)
  await proveNativeToolOutputFilePaths({
    context: capture.context,
    callId,
    previewText: receipt.previewText,
    previewMarkers,
    absentMarkers: rowAbsentMarkers,
    paths: receipt.paths,
    status: 'completed',
    prepareView: options.prepareView ?? expandNativeResultView,
    workerProof: () => expectUnchangedNativeRecord(capture.context, capture.agent, snapshot => read(snapshot, callId), receipt),
  })
}
