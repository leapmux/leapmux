import { requireRenovate } from './renovate-engine.mjs'

// Reads the version ranges and the patched versions of GitHub advisory records. The module
// loads only Renovate's semver, so the dashboard, which Renovate loads under Node, can read
// an advisory without the module graph of the exemptions.
const semver = requireRenovate('semver')

/** Remove the Go spelling of a version, so that a semver range can match it. */
export function normalizeAdvisoryVersion(version) {
  return String(version).replace(/^v/, '').replace(/\+incompatible$/, '')
}

/**
 * Return a GitHub advisory range as a semver range, or null when the advisory states none.
 * GitHub separates the comparators of one range with commas. A blank range is null: semver
 * reads an empty range as every version.
 */
export function advisoryRange(range) {
  if (typeof range !== 'string' || range.trim() === '')
    return null
  const normalized = range.replaceAll(',', ' ')
  return semver.validRange(normalized)
    ? normalized
    : null
}

/** Match a version against an advisory range, including prereleases such as Go pseudo-versions. */
export function isInAdvisoryRange(version, range) {
  const normalized = advisoryRange(range)
  if (normalized === null)
    throw new Error(`Invalid advisory range: ${JSON.stringify(range ?? null)}`)
  const candidate = semver.valid(normalizeAdvisoryVersion(version))
  return candidate !== null && semver.satisfies(candidate, normalized, { includePrerelease: true })
}

/** Return the first patched version of an advisory record, or null when the record states none. */
export function patchedIdentifier(record) {
  const value = record?.first_patched_version
  return value !== null && typeof value === 'object'
    ? value.identifier ?? null
    : value ?? null
}
