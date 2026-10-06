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
import { getGlobalState } from './server'
import { openWorkspace, readAttachedWithArgument, toolCallRow } from './ui'

export interface NativeToolOutputFilePathsOptions {
  context: Pick<ManagedNativeScenarioContext, 'page' | 'workspaceId'>
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
   * Bring the result row into the rendered rows of the chat before the view proof counts it. The chat renders only
   * the rows near its scroll position, so a row far above the end has no element until the chat scrolls to it.
   */
  revealView?: () => Promise<void>
  /** Prepare the counted result row for the marker checks, for example expand it. */
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
  // A preview marker is part of the preview, so an absent marker that the preview holds also contradicts a preview
  // marker that holds it. One check refuses both.
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
 * A marked element that wraps the path block or sits inside it fails the proof, because it can hide output before the paths.
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
  const result = toolCallRow(options.context.page, options.callId)
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
 * Check one receipt against the computed output. Return its one declared path, the markers of its preview, the line
 * that its preview must not hold, and the lines that its result row must not show.
 *
 * The receipt must declare exactly one path, and its preview must not hold the absent line: the omitted middle line
 * by default. Without `previewMarkers`, the markers are the first line and the last line of the output that the
 * preview holds, and the preview must hold at least one of them.
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
 * Require the selected agent, its native session, and the Worker record that `read` selects, all unchanged.
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
  // Playwright selects its matchers from the type of the value. A type parameter leaves that choice open, so the
  // value goes through `unknown`, which takes the generic matchers.
  const record: unknown = await read(snapshot)
  expect(record, 'the Worker record of the native result stays the same').toEqual(expected)
}

/** Optional facts of one receipt proof. */
export interface NativeOutputReceiptOptions<R extends NativeOutputReceipt> extends NativeOutputReceiptMarkers<R> {
  /** The directory that must hold the declared path. The E2E run directory by default. */
  privateRoot?: string
  /** Prepare the result row for the view proof. `expandNativeResultView` by default. */
  prepareView?: (result: Locator) => Promise<void>
  /** A provider proof of the first receipt. It runs after the receipt checks and before the view proof. */
  extraProof?: (receipt: R) => void | Promise<void>
}

/**
 * Prove one declared private output path, its native preview, and the unchanged Worker record, before and after a
 * reload.
 *
 * `read` is the provider's output path reader. The proof reads the receipt from the captured snapshot, attaches
 * it, and requires one private path and a preview without the absent line: the omitted middle line by default.
 * The result row must hold the preview markers and must not show the absent line, unless the arguments of the call
 * hold the output (`argumentsHoldOutput`). On each pass, the Worker proof reads the receipt again through `read` and
 * requires it unchanged as a whole.
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
