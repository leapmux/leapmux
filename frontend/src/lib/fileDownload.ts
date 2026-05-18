import type { SaveStreamHandle } from '~/api/platformBridge'
import type { PathFlavor } from '~/lib/paths'
import { platformBridge } from '~/api/platformBridge'
import * as workerRpc from '~/api/workerRpc'
import { basename } from '~/lib/paths'

// 1 MiB chunks: large enough to keep round-trip count manageable for
// multi-megabyte files, small enough to stay well under the worker's
// max-message ceiling and to bound the IPC clone peak when each chunk
// is piped through `platformBridge.fileSaveWrite`.
const DOWNLOAD_CHUNK_SIZE = 1 << 20

/**
 * `total` is `-1` until the worker's first response delivers `totalSize`;
 * callers should render an indeterminate state until then.
 */
export type DownloadProgress = (received: number, total: number) => void

/**
 * Hub-blindness invariant for the download path.
 *
 * File content traverses the Noise_NK encrypted channel established by
 * `ChannelService.OpenChannel`. The relay Hub sees only the outer
 * `ChannelMessage` envelopes (channel_id, correlation_id, flags,
 * ciphertext bytes). Hub cannot decrypt the file content, the file
 * path inside `ReadFileRequest`, the RPC method name (`ReadFile`), or
 * the `total_size` field in `ReadFileResponse`.
 *
 * Hub CAN observe — same as the inline file viewer — the destination
 * `worker_id`, per-chunk envelope sizes (summed to recover the file
 * size to chunk resolution), the chunk count for >1 MiB files, and
 * per-chunk timing (worker I/O latency). Unlike the inline viewer,
 * the download path issues `readFile` calls only — no `statFile`
 * precedes the burst, so the "stat then read" signature is absent.
 */

interface FileChunk {
  /**
   * Newly read bytes (not concatenated with prior chunks). Backed by a
   * plain `ArrayBuffer` (not `SharedArrayBuffer`) so consumers can hand
   * it straight to `Blob` / Tauri IPC without a second copy.
   */
  chunk: Uint8Array<ArrayBuffer>
  /** Cumulative bytes received including this chunk. */
  received: number
  /** Total file size from the first response, or `-1` before that. */
  totalSize: number
}

/**
 * Stream `filePath` from the worker, yielding each `readFile` response
 * as a `FileChunk`. The first response carries `totalSize`; once known
 * the loop bound switches from "until empty" to "until totalSize". No
 * accumulator is allocated — callers that need the full bytes must
 * concatenate themselves. Each yielded chunk is laundered through
 * `ensurePlainArrayBufferView` so callers can hand it to `Blob` / Tauri
 * IPC without an extra copy.
 */
async function* streamFileChunks(
  workerId: string,
  filePath: string,
): AsyncGenerator<FileChunk, void, void> {
  let received = 0
  let totalSize = -1
  while (totalSize < 0 || received < totalSize) {
    const resp = await workerRpc.readFile(workerId, {
      workerId,
      path: filePath,
      offset: BigInt(received),
      limit: BigInt(DOWNLOAD_CHUNK_SIZE),
    })
    if (totalSize < 0)
      totalSize = Number(resp.totalSize)
    const view = resp.content
    if (view.length === 0)
      break
    const chunk = ensurePlainArrayBufferView(view)
    received += chunk.length
    yield { chunk, received, totalSize }
  }
}

/**
 * Pass-through if `view` already covers an entire plain `ArrayBuffer`;
 * otherwise copy into a fresh `ArrayBuffer`. Avoids the per-chunk
 * memcpy on the common protobuf-runtime case (full-buffer view, plain
 * ArrayBuffer backing) while still defending against `SharedArrayBuffer`
 * backings and surprise-aliasing sub-slices that `Blob`/Tauri callers
 * downstream would otherwise inherit.
 */
function ensurePlainArrayBufferView(view: Uint8Array): Uint8Array<ArrayBuffer> {
  if (
    view.buffer instanceof ArrayBuffer
    && view.byteOffset === 0
    && view.byteLength === view.buffer.byteLength
  ) {
    return view as Uint8Array<ArrayBuffer>
  }
  const copy = new Uint8Array(view.length)
  copy.set(view)
  return copy
}

/**
 * Minimum gap between intermediate `onProgress` callbacks. The first
 * chunk and the final chunk always fire so the spinner labels its
 * starting and ending state precisely; in between we coalesce to one
 * fire per ~100 ms so a 100-chunk save renders ~10 progress events
 * instead of 100 (each event drives a Solid signal write + render).
 */
const PROGRESS_THROTTLE_MS = 100

/**
 * Wrap `onProgress` so intermediate calls coalesce to one per
 * `intervalMs`. The first call (`lastEmittedAt === -Infinity` is
 * trivially past the gap) and the final call (signaled by
 * `total >= 0 && received >= total`) always pass through, so the
 * spinner labels its starting and ending state precisely. Returns
 * `undefined` when `onProgress` itself is undefined, so call sites
 * can keep the `?.()` short-circuit.
 */
function throttleProgress(
  onProgress: DownloadProgress | undefined,
  intervalMs: number,
): DownloadProgress | undefined {
  if (!onProgress)
    return undefined
  let lastEmittedAt = Number.NEGATIVE_INFINITY
  return (received, total) => {
    const now = performance.now()
    const isFinal = total >= 0 && received >= total
    if (isFinal || now - lastEmittedAt >= intervalMs) {
      onProgress(received, total)
      lastEmittedAt = now
    }
  }
}

/**
 * Web-mode download: stream chunks into a `Blob` and trigger a browser
 * anchor click. All chunks stay alive in `parts` until the `Blob` is
 * constructed, but memory is fragmented across N × 1 MiB views rather
 * than one contiguous `Uint8Array(totalSize)` — which lets large files
 * complete on engines that fail the single contiguous allocation. The
 * browser owns the destination directory; the caller cannot know where
 * the file landed.
 *
 * `flavor` skips the `detectFlavor` sniff inside `basename` when the
 * caller already knows it (keeps the download filename consistent with
 * the toast label).
 */
export async function downloadFileFromWorker(
  workerId: string,
  filePath: string,
  flavor?: PathFlavor,
  onProgress?: DownloadProgress,
): Promise<void> {
  const emit = throttleProgress(onProgress, PROGRESS_THROTTLE_MS)
  const parts: BlobPart[] = []
  let lastReceived = 0
  let lastTotalSize = 0
  for await (const { chunk, received, totalSize } of streamFileChunks(workerId, filePath)) {
    parts.push(chunk)
    emit?.(received, totalSize)
    lastReceived = received
    lastTotalSize = totalSize
  }
  // Truncation: worker stopped before delivering `totalSize` bytes, so
  // the throttle's isFinal trigger never fired. Force a final emit at
  // (received, received) — total=received makes isFinal true and the
  // spinner reaches 100% for the actual bytes we got.
  if (lastReceived < lastTotalSize)
    emit?.(lastReceived, lastReceived)
  const url = URL.createObjectURL(new Blob(parts))
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = basename(filePath, flavor)
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  }
  finally {
    // Revoke after the browser has had a chance to start the download.
    // 1s is conservative — most browsers latch the bytes synchronously
    // on `click()`, but `setTimeout` here keeps us safe across engines.
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
}

/**
 * Pipe streamed chunks into an already-open save handle. Keeps at most
 * one outstanding `fileSaveWrite` while the next worker `readFile` is
 * in flight — bounds peak memory to two chunks (~2 MiB) while halving
 * the round-trip stalls of strictly sequential issue/await.
 *
 * Async generators are pull-based, so a plain `for await` would only
 * start the next `readFile` after awaiting the previous `fileSaveWrite`
 * — defeating the overlap. We pre-issue `iterator.next()` *before*
 * awaiting the pending write so the worker read and the disk write run
 * concurrently.
 *
 * On any error mid-stream, drains the outstanding write, aborts the
 * handle so the Rust side removes the partial file, and re-throws the
 * original error. Abort errors on the failure path are swallowed so
 * they don't mask the cause; on success a commit error propagates
 * (a rename failure is itself a save failure).
 */
async function pumpChunksToHandle(
  workerId: string,
  filePath: string,
  handle: SaveStreamHandle,
  onProgress: DownloadProgress | undefined,
): Promise<void> {
  const emit = throttleProgress(onProgress, PROGRESS_THROTTLE_MS)
  const reader = streamFileChunks(workerId, filePath)
  let pendingWrite: Promise<void> | undefined
  let nextRead = reader.next()
  let lastReceived = 0
  let lastTotalSize = 0
  try {
    while (true) {
      const result = await nextRead
      if (result.done)
        break
      const { chunk, received, totalSize } = result.value
      // Kick off the next worker read *before* awaiting the prior write,
      // so the read and write overlap on the wire.
      nextRead = reader.next()
      if (pendingWrite)
        await pendingWrite
      pendingWrite = platformBridge.fileSaveWrite(handle.id, chunk)
      emit?.(received, totalSize)
      lastReceived = received
      lastTotalSize = totalSize
    }
    // Drain the final outstanding write before declaring success.
    if (pendingWrite)
      await pendingWrite
    // Truncation: worker stopped early so throttle's isFinal trigger
    // never fired. Force a final emit at (received, received) so the
    // spinner reaches 100% for the bytes we actually wrote.
    if (lastReceived < lastTotalSize)
      emit?.(lastReceived, lastReceived)
    await platformBridge.fileSaveCommit(handle.id)
  }
  catch (err) {
    // Drain any still-in-flight read and write before abort — otherwise
    // `fileSaveAbort` races with `fileSaveWrite` on the Rust side, and
    // a stray `readFile` rejection becomes an unhandled promise. Swallow
    // both drains and the abort error so they don't mask the cause.
    await nextRead.catch(() => {})
    if (pendingWrite)
      await pendingWrite.catch(() => {})
    await platformBridge.fileSaveAbort(handle.id).catch(() => {})
    throw err
  }
}

/**
 * Desktop-mode silent save: open a destination in the user's OS
 * Downloads directory, then pipe each worker chunk through
 * `fileSaveWrite`. Returns the absolute path written. Caller may then
 * pass the path to `platformBridge.revealInFileManager`.
 *
 * Peak memory is bounded to two chunks (~2 MiB total, the pipelined
 * "just-read + in-flight write" pair from `pumpChunksToHandle`) across
 * the JS heap + IPC + Rust clone chain, so the transient cost stays in
 * single-digit MB even for multi-hundred-MB files.
 */
export async function saveFileToDownloads(
  workerId: string,
  filePath: string,
  flavor?: PathFlavor,
  onProgress?: DownloadProgress,
): Promise<string> {
  const handle = await platformBridge.fileSaveOpen(basename(filePath, flavor))
  await pumpChunksToHandle(workerId, filePath, handle, onProgress)
  return handle.path
}

/**
 * Desktop-mode save-as: show a native save dialog *first*, then stream
 * the bytes to the chosen path. Returns `null` if the user cancelled
 * the dialog — in that case the worker is never asked for the file, so
 * a cancellation costs nothing in fetch + memory time.
 */
export async function saveFileAs(
  workerId: string,
  filePath: string,
  flavor?: PathFlavor,
  onProgress?: DownloadProgress,
): Promise<string | null> {
  const handle = await platformBridge.fileSaveOpenDialog(basename(filePath, flavor))
  if (handle === null)
    return null
  await pumpChunksToHandle(workerId, filePath, handle, onProgress)
  return handle.path
}
