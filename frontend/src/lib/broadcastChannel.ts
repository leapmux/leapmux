/**
 * Construct a `BroadcastChannel`, or answer null where there is none.
 *
 * TWO WAYS IT CAN BE ABSENT, and only one of them is the obvious one. The
 * constructor may not exist at all -- an old browser, a server-side render. Or
 * it may exist and REFUSE TO CONSTRUCT: some embedded webviews expose the class
 * and throw when it is called. A caller that tested only `typeof` therefore
 * throws at module evaluation on exactly the platforms it meant to tolerate.
 *
 * Every consumer here degrades the same way -- it loses cross-tab sync and
 * nothing else -- so this reports the reason and answers null rather than
 * rejecting. `onUnavailable` takes the reason and, for the refusal, the error,
 * because a caller that logs wants both and a caller that does not can omit it.
 */
export function tryCreateBroadcastChannel(
  name: string,
  onUnavailable?: (reason: string, err?: unknown) => void,
): BroadcastChannel | null {
  if (typeof BroadcastChannel === 'undefined') {
    onUnavailable?.('no BroadcastChannel')
    return null
  }
  try {
    return new BroadcastChannel(name)
  }
  catch (err) {
    onUnavailable?.('BroadcastChannel refused to construct', err)
    return null
  }
}
