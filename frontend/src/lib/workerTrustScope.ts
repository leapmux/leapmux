/** One adopted account lifetime for Worker trust decisions. */
export interface WorkerTrustScope {
  userId: string | null
  generation: number
  signal: AbortSignal
}

let readScope: (() => WorkerTrustScope) | undefined

/** Register the AuthProvider-owned scope reader. */
export function setWorkerTrustScopeReader(read: () => WorkerTrustScope): () => void {
  readScope = read
  return () => {
    if (readScope === read)
      readScope = undefined
  }
}

/** The scope adopted by the live AuthProvider. */
export function currentWorkerTrustScope(): WorkerTrustScope | undefined {
  return readScope?.()
}
