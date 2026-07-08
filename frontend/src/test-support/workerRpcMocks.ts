/**
 * Shared shape for mocked `workerRpc.readFile` responses.
 *
 * Both `content` and `totalSize` mirror the protobuf wire shape:
 *
 *   - `content` is a `Uint8Array` of the bytes for this chunk
 *   - `totalSize` is a `BigInt` of the whole file size (the worker
 *     responds with this on every chunk so the caller can detect EOF
 *     even when a chunk arrives short)
 *
 * `totalSize` defaults to `content.length` so callers building a
 * single-chunk response don't have to repeat themselves.
 */
export function readResp(content: Uint8Array, totalSize?: number) {
  return {
    content,
    totalSize: BigInt(totalSize ?? content.length),
  }
}

/**
 * Shared shape for mocked `workerRpc.statFile` responses — just the
 * `info.size` field as a BigInt, matching the protobuf wire shape.
 */
export function statResp(size: number) {
  return { info: { size: BigInt(size) } }
}
