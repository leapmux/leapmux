import { DIRAC_RECORD_FIELD as FIELD, DIRAC_RECORD_KIND as KIND } from '../../../src/generated/contracts/dirac-protocol'
import { isObject, pickString } from '../../../src/lib/jsonPick'

/** Read the exact native tasklet addressed by this owned child transcript. */
export function diracTaskletIdFromRecords(records: readonly unknown[], parentSessionId: string, childSessionId: string): string {
  if (!parentSessionId || !childSessionId || parentSessionId === childSessionId)
    throw new Error('The native Dirac tasklet requires distinct parent and child sessions.')
  const identities = new Set<string>()
  for (const record of records) {
    if (!isObject(record) || (record[FIELD.Type] !== KIND.Tool && record[FIELD.Type] !== KIND.Message))
      continue
    const id = pickString(record, FIELD.TaskletID)
    if (!id.trim() || record[FIELD.SessionID] !== childSessionId || record[FIELD.ParentSessionID] !== parentSessionId)
      throw new Error('The native Dirac tasklet record belongs to another conversation.')
    identities.add(id)
  }
  const [identity, ...others] = identities
  if (identity === undefined || others.length !== 0)
    throw new Error('The native Dirac transcript must identify exactly one tasklet.')
  return identity
}
