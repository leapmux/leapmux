#!/usr/bin/env node
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { isDeepStrictEqual } from 'node:util'
import { readGithubJsonResponse } from './github-json.mjs'

const administratorBypass = Object.freeze({ actor_type: 'RepositoryRole', actor_id: 5, bypass_mode: 'always' })
const reviewRequirements = Object.freeze({
  required_approving_review_count: 1,
  dismiss_stale_reviews_on_push: true,
  require_code_owner_review: false,
  require_last_push_approval: true,
  required_review_thread_resolution: false,
})
const checkRequirements = Object.freeze({
  strict_required_status_checks_policy: true,
  do_not_enforce_on_create: false,
})
const requiredChecks = Object.freeze(['Required CI', 'NOTICE'].map(context => Object.freeze({ context, integration_id: 15368 })))

/** Apply only after the reviewed implementation and both required checks reach main. */
export const RENOVATE_MAIN_RULESET = {
  name: 'Renovate review and CI',
  target: 'branch',
  enforcement: 'active',
  conditions: {
    ref_name: {
      include: ['refs/heads/main'],
      exclude: [],
    },
  },
  bypass_actors: [
    { ...administratorBypass },
  ],
  rules: [
    {
      type: 'pull_request',
      parameters: { ...reviewRequirements },
    },
    {
      type: 'required_status_checks',
      parameters: {
        ...checkRequirements,
        required_status_checks: requiredChecks.map(check => ({ ...check })),
      },
    },
  ],
}

/** Validate the effective main rules and every contributing ruleset's bypass actors. */
export function checkProtection(effective, rulesets) {
  if (!Array.isArray(effective) || !Array.isArray(rulesets))
    throw new TypeError('Protection responses must be arrays.')
  const required = [
    'deletion',
    'non_fast_forward',
    'required_linear_history',
    'pull_request',
    'required_status_checks',
  ]
  for (const type of required) {
    if (!effective.some(rule => rule.type === type))
      throw new Error(`main lacks the ${type} rule.`)
  }
  for (const rule of effective) {
    const source = rulesets.find(set => set.id === rule.ruleset_id)
    if (!source || source.enforcement !== 'active')
      throw new Error(`Cannot verify active ruleset ${rule.ruleset_id}.`)
    if (!Array.isArray(source.bypass_actors))
      throw new TypeError(`Ruleset ${source.id} omits bypass actors.`)
    for (const actor of source.bypass_actors) {
      if (Object.entries(administratorBypass).some(([key, value]) => actor[key] !== value))
        throw new Error(`Ruleset ${source.id} permits a bypass other than repository administrators.`)
    }
    if ((rule.type === 'pull_request' || rule.type === 'required_status_checks') && source.bypass_actors.length === 0)
      throw new Error(`Ruleset ${source.id} prevents the administrator from pushing an approved release.`)
  }
  const reviews = effective.filter(rule => rule.type === 'pull_request')
  if (!reviews.some(({ parameters }) => parameters && Object.entries(reviewRequirements).every(([key, value]) =>
    typeof value === 'number'
      ? parameters[key] >= value
      : !value || parameters[key] === value))) {
    throw new Error('main requires one review, stale-review dismissal, latest-push approval, and administrator bypass.')
  }
  for (const { context, integration_id } of requiredChecks) {
    if (!effective.some(rule => rule.type === 'required_status_checks'
      && rule.parameters?.strict_required_status_checks_policy === checkRequirements.strict_required_status_checks_policy
      && rule.parameters?.required_status_checks?.some(check => check.context === context && check.integration_id === integration_id))) {
      throw new Error(`main lacks the strict ${context} check from GitHub Actions (${integration_id}).`)
    }
  }
}

function rulesetMetadata(ruleset) {
  if (!ruleset || !Number.isSafeInteger(ruleset.id) || ruleset.id <= 0)
    throw new Error('Protection requires positive integer ruleset IDs.')
  const metadata = structuredClone(ruleset)
  delete metadata.bypass_actors
  delete metadata.current_user_can_bypass
  // Administrator and App responses can express the same revision in different timezones.
  for (const key of ['created_at', 'updated_at']) {
    if (key === 'created_at' && metadata[key] === undefined)
      continue
    if (typeof metadata[key] !== 'string' || !/^\d{4}-\d{2}-\d{2}T.+(?:Z|[+-]\d{2}:\d{2})$/.test(metadata[key]) || !Number.isFinite(Date.parse(metadata[key])))
      throw new Error(`Ruleset ${ruleset.id} lacks a valid ${key} revision timestamp.`)
    metadata[key] = new Date(metadata[key]).toISOString()
  }
  return metadata
}

function requireMatchingSources(effective, rulesets) {
  if (!Array.isArray(effective) || !Array.isArray(rulesets) || rulesets.length === 0)
    throw new Error('Protection requires nonempty ruleset sources.')
  const ids = new Set(effective.map(rule => rule.ruleset_id))
  const sources = new Set(rulesets.map(rule => rule?.id))
  if ([
    ...ids,
    ...sources,
  ].some(id => !Number.isSafeInteger(id) || id <= 0)
  || sources.size !== rulesets.length || sources.size !== ids.size || [...ids].some(id => !sources.has(id))) {
    throw new Error('The protection snapshot must cover exactly the effective ruleset sources.')
  }
}

/** Capture only protection that an administrator has read and validated. */
export function createProtectionSnapshot(effective, rulesets) {
  requireMatchingSources(effective, rulesets)
  checkProtection(effective, rulesets)
  return {
    version: 1,
    repository: 'leapmux/leapmux',
    ref: 'refs/heads/main',
    rulesets: rulesets.map(rule => ({
      ...rulesetMetadata(rule),
      bypass_actors: structuredClone(rule.bypass_actors),
    })),
  }
}

/**
 * GitHub hides bypass actors from App tokens, so the administrator snapshot supplies them.
 * Accept a ruleset's recorded actors only when its complete public metadata and revision
 * timestamp match the snapshot. A changed, missing, or additional ruleset fails.
 */
function restoreSnapshotActors(snapshot, effective, rulesets) {
  if (typeof snapshot === 'string') {
    try {
      snapshot = JSON.parse(snapshot)
    }
    catch {
      throw new Error('RENOVATE_PROTECTION_SNAPSHOT must contain valid JSON.')
    }
  }
  if (!snapshot || snapshot.version !== 1 || snapshot.repository !== 'leapmux/leapmux' || snapshot.ref !== 'refs/heads/main')
    throw new Error('The protection snapshot requires version 1 and leapmux/leapmux main.')
  requireMatchingSources(effective, snapshot.rulesets)
  return rulesets.map((live) => {
    const saved = snapshot.rulesets.find(rule => rule.id === live.id)
    if (!saved || !isDeepStrictEqual(rulesetMetadata(live), rulesetMetadata(saved)))
      throw new Error(`Ruleset ${live.id} changed; an administrator must refresh RENOVATE_PROTECTION_SNAPSHOT.`)
    if (Object.hasOwn(live, 'bypass_actors') && !isDeepStrictEqual(live.bypass_actors, saved.bypass_actors))
      throw new Error(`Ruleset ${live.id} bypass actors differ from the administrator snapshot.`)
    return { ...live, bypass_actors: saved.bypass_actors }
  })
}

/** Both the administrator CLI and the read-only App read the same effective sources. */
export async function readRepositoryProtection(get) {
  const effective = []
  for (let page = 1; ; page++) {
    const batch = await get(`/repos/leapmux/leapmux/rules/branches/main?per_page=100&page=${page}`)
    if (!Array.isArray(batch))
      throw new TypeError('GitHub returned an invalid effective rule list.')
    effective.push(...batch)
    if (batch.length < 100)
      break
    if (page >= 100)
      throw new Error('Effective protection exceeds the supported rule count.')
  }
  const ids = [
    ...new Set(effective.map(rule => rule.ruleset_id)),
  ]
  if (ids.some(id => !Number.isSafeInteger(id) || id <= 0))
    throw new Error('GitHub returned an invalid ruleset source ID.')
  const rulesets = await Promise.all(ids.map(id => get(`/repos/leapmux/leapmux/rulesets/${id}`)))
  requireMatchingSources(effective, rulesets)
  return { effective, rulesets }
}

export async function verifyRepository({ token = process.env.GH_TOKEN, repository = 'leapmux/leapmux', request = fetch, snapshot = process.env.RENOVATE_PROTECTION_SNAPSHOT } = {}) {
  if (repository !== 'leapmux/leapmux' || !token)
    throw new Error('Protection verification requires leapmux/leapmux and a read-only token.')
  const freshRequest = (url, options) => request(url, {
    ...options,
    cache: 'no-store',
    headers: { ...options.headers, 'Cache-Control': 'no-cache' },
  })
  const get = async path => (await readGithubJsonResponse(path, { token, request: freshRequest, description: 'GitHub protection read' })).data
  const { effective, rulesets } = await readRepositoryProtection(get)
  const verified = snapshot
    ? restoreSnapshotActors(snapshot, effective, rulesets)
    : rulesets
  checkProtection(effective, verified)
  return { effective, rulesets: verified }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    if (process.argv[2] === '--print-ruleset') {
      console.log(JSON.stringify(RENOVATE_MAIN_RULESET, null, 2))
      process.exit(0)
    }
    if (process.argv.length > 2)
      throw new Error('Only --print-ruleset is supported.')
    await verifyRepository()
    console.log('main requires human review and CI; only repository administrators can bypass its rules.')
  }
  catch (error) {
    console.error(error.message)
    process.exitCode = 1
  }
}
