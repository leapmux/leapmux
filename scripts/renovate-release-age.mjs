import { renovateModule } from './renovate-engine.mjs'

const { calculateMinimumReleaseAgeMs } = await renovateModule('util/minimum-release-age.js')
const { getConfig: renovateDefaults } = await renovateModule('config/defaults.js')

const AGE_OPTIONS = ['minimumReleaseAge', 'minimumReleaseAgeBuffer']
const ageDefaults = Object.freeze(Object.fromEntries(AGE_OPTIONS.map(option => [option, renovateDefaults()[option]])))

/**
 * Return the release age that the policy requires, as Renovate computes it, including any buffer.
 * An option that the policy omits takes Renovate's default, as in Renovate's own merge: an
 * omitted buffer adds Renovate's default buffer, and only an explicit null removes it.
 */
export function releaseAgeMs(config) {
  const effective = { ...ageDefaults }
  for (const option of AGE_OPTIONS) {
    if (config[option] !== undefined)
      effective[option] = config[option]
  }
  const age = calculateMinimumReleaseAgeMs(effective)
  if (!Number.isSafeInteger(age) || age <= 0)
    throw new Error(`The minimum release age must be a positive duration: ${JSON.stringify(config.minimumReleaseAge ?? null)}`)
  return age
}

/**
 * Return the release age of a security fix, as Renovate computes it from the policy's
 * `vulnerabilityAlerts` and the policy's buffer, or 0 when the policy sets no such age, as
 * Renovate's own default does.
 */
export function securityReleaseAgeMs(config) {
  const age = config.vulnerabilityAlerts?.minimumReleaseAge
  return age == null
    ? 0
    : releaseAgeMs({ minimumReleaseAge: age, minimumReleaseAgeBuffer: config.minimumReleaseAgeBuffer })
}

const AGE_UNITS = Object.freeze([
  ['day', 86400000],
  ['hour', 3600000],
  ['minute', 60000],
  ['second', 1000],
  ['millisecond', 1],
])

/** Describe a release age in words, such as `3 days` or `3 days 30 minutes`. */
export function describeAge(ageMs) {
  if (!Number.isSafeInteger(ageMs) || ageMs <= 0)
    throw new Error('A release age description requires a positive duration.')
  const parts = []
  let rest = ageMs
  for (const [unit, size] of AGE_UNITS) {
    const count = Math.floor(rest / size)
    rest -= count * size
    if (count > 0) {
      parts.push(`${count} ${unit}${count === 1
        ? ''
        : 's'}`)
    }
  }
  return parts.join(' ')
}

/** Return the latest publication time that the release age admits at `now`. */
export function publishTimeCutoff(ageMs, now = Date.now()) {
  if (!Number.isSafeInteger(ageMs) || ageMs <= 0 || !Number.isFinite(now))
    throw new Error('A publication cutoff requires a positive release age and a finite time.')
  return new Date(now - ageMs)
}

/**
 * Return whether a release that was published at `publishedMs` meets the release age whose
 * cutoff is `cutoffMs`, the latest publication time that the age admits. A release without
 * a finite publication time never meets it.
 */
export function isPublishedBy(publishedMs, cutoffMs) {
  return Number.isFinite(publishedMs) && publishedMs <= cutoffMs
}
