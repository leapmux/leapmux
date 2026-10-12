/**
 * The words of the system prompt of a Factory Droid child, which runs a read-only exploration.
 * A rule that requires them answers the own turn of a child and never the turn of its root.
 *
 * The module imports nothing. `scenarios.ts` imports it, and the Droid test object imports `scenarios.ts`, so an
 * import of the test object here would close an import cycle.
 */
export const DROID_CHILD_SYSTEM = 'READ-ONLY exploration'
