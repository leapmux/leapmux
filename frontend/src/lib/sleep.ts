/** Resolve after `ms` milliseconds. A promisified `setTimeout`. */
export function sleep(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms))
}
