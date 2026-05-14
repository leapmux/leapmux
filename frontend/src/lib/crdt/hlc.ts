import type { HLC } from '~/generated/leapmux/v1/org_crdt_pb'
import { create } from '@bufbuild/protobuf'
import { HLCSchema } from '~/generated/leapmux/v1/org_crdt_pb'

/** Compare two HLC values lex by (physical, logical, client_id). */
export function hlcCmp(a: HLC | undefined | null, b: HLC | undefined | null): number {
  if (!a && !b)
    return 0
  if (!a)
    return -1
  if (!b)
    return 1
  if (a.physical < b.physical)
    return -1
  if (a.physical > b.physical)
    return 1
  if (a.logical < b.logical)
    return -1
  if (a.logical > b.logical)
    return 1
  if (a.clientId < b.clientId)
    return -1
  if (a.clientId > b.clientId)
    return 1
  return 0
}

/** Reports whether an HLC is unset (undefined / null / all-zero). */
export function hlcIsZero(h: HLC | undefined | null): boolean {
  if (!h)
    return true
  return h.physical === 0n && h.logical === 0n && h.clientId === ''
}

/** Deep-clone an HLC. */
export function hlcClone(h: HLC | undefined | null): HLC | undefined {
  if (!h)
    return undefined
  return create(HLCSchema, { physical: h.physical, logical: h.logical, clientId: h.clientId })
}

/**
 * HLCClock is a hybrid logical clock. The frontend mints `client_hlc`
 * advisory hints with this clock; the canonical HLCs come from the
 * hub on echo, and the clock absorbs them via observe().
 */
export class HLCClock {
  private maxPhys = 0n
  private maxLog = 0n

  constructor(public readonly clientId: string) {}

  tick(now?: number): HLC {
    const nowMs = BigInt(now ?? Date.now())
    if (nowMs > this.maxPhys) {
      this.maxPhys = nowMs
      this.maxLog = 0n
    }
    else {
      this.maxLog++
    }
    return create(HLCSchema, {
      physical: this.maxPhys,
      logical: this.maxLog,
      clientId: this.clientId,
    })
  }

  observe(remote: HLC | undefined | null): void {
    if (!remote)
      return
    const rp = remote.physical
    const rl = remote.logical
    if (rp > this.maxPhys) {
      this.maxPhys = rp
      this.maxLog = rl
      return
    }
    if (rp === this.maxPhys && rl > this.maxLog) {
      this.maxLog = rl
    }
  }

  current(): HLC {
    return create(HLCSchema, {
      physical: this.maxPhys,
      logical: this.maxLog,
      clientId: this.clientId,
    })
  }
}
