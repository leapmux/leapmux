import type { PathFlavor } from '~/lib/paths'
import { platformBridge } from '~/api/platformBridge'
import * as workerRpc from '~/api/workerRpc'
import { basename } from '~/lib/paths'

// 1 MiB chunks: large enough to keep round-trip count manageable for
// multi-megabyte files, small enough to stay well under the worker's
// max-message ceiling.
const DOWNLOAD_CHUNK_SIZE = 1 << 20

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

/**
 * Stream `filePath` from the worker, accumulating chunks into a single
 * in-memory `Uint8Array`. The first response's `totalSize` drives the
 * loop bound; no separate `statFile` round-trip is needed.
 */
async function fetchFileBytes(workerId: string, filePath: string): Promise<Uint8Array> {
  // First response carries totalSize; once it's known we allocate the
  // final ArrayBuffer-backed Uint8Array and `set` each chunk into it
  // directly. The protobuf-runtime chunk view's buffer type
  // (`ArrayBufferLike`) admits `SharedArrayBuffer`, which `Blob` / Tauri
  // serialization reject — copying into our fresh ArrayBuffer-backed
  // `merged` simultaneously launders that and concatenates.
  let merged: Uint8Array | null = null
  let received = 0
  let totalSize = -1
  while (totalSize < 0 || received < totalSize) {
    const resp = await workerRpc.readFile(workerId, {
      workerId,
      path: filePath,
      offset: BigInt(received),
      limit: BigInt(DOWNLOAD_CHUNK_SIZE),
    })
    if (totalSize < 0) {
      totalSize = Number(resp.totalSize)
      merged = new Uint8Array(totalSize)
    }
    const chunk = resp.content
    if (chunk.length === 0)
      break
    merged!.set(chunk, received)
    received += chunk.length
  }
  if (merged === null)
    return new Uint8Array(0)
  // Worker truncated below the advertised size (file shrank
  // mid-download, sparse-file edge case): trim to what we actually got.
  if (received < merged.length)
    return merged.subarray(0, received)
  return merged
}

/**
 * Web-mode download: assemble bytes into a Blob and trigger a browser
 * anchor click. The browser owns the destination directory; the caller
 * cannot know where the file landed. Pass `flavor` to skip the
 * `detectFlavor` sniff inside `basename` when the caller already knows
 * it (keeps the download filename consistent with the toast label).
 */
export async function downloadFileFromWorker(
  workerId: string,
  filePath: string,
  flavor?: PathFlavor,
): Promise<void> {
  const bytes = await fetchFileBytes(workerId, filePath)
  const url = URL.createObjectURL(new Blob([bytes as BlobPart]))
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
 * Desktop-mode silent save: write the streamed bytes into the user's
 * OS Downloads directory and return the absolute path written.
 * Caller may then pass the path to `platformBridge.revealInFileManager`.
 */
export async function saveFileToDownloads(
  workerId: string,
  filePath: string,
  flavor?: PathFlavor,
): Promise<string> {
  const bytes = await fetchFileBytes(workerId, filePath)
  return platformBridge.saveBytesToDownloads(bytes, basename(filePath, flavor))
}

/**
 * Desktop-mode save-as: show a native save dialog, write the bytes to
 * the chosen path, and return the absolute path. Returns `null` if the
 * user cancelled the dialog.
 */
export async function saveFileAs(
  workerId: string,
  filePath: string,
  flavor?: PathFlavor,
): Promise<string | null> {
  const bytes = await fetchFileBytes(workerId, filePath)
  return platformBridge.saveBytesAs(bytes, basename(filePath, flavor))
}
