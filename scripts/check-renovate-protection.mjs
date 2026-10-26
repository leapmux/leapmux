#!/usr/bin/env node
import process from 'node:process'
import { pathToFileURL } from 'node:url'

/** Apply only after the reviewed implementation and both required checks reach main. */
export const RENOVATE_MAIN_RULESET = {
  name: 'Renovate review and CI',
  target: 'branch',
  enforcement: 'active',
  conditions: { ref_name: { include: ['refs/heads/main'], exclude: [] } },
  bypass_actors: [{ actor_type: 'RepositoryRole', actor_id: 5, bypass_mode: 'always' }],
  rules: [
    { type: 'pull_request', parameters: {
      required_approving_review_count: 1,
      dismiss_stale_reviews_on_push: true,
      require_code_owner_review: false,
      require_last_push_approval: true,
      required_review_thread_resolution: false,
    } },
    { type: 'required_status_checks', parameters: {
      required_status_checks: ['Required CI', 'NOTICE'].map(context => ({ context, integration_id: 15368 })),
      strict_required_status_checks_policy: true,
      do_not_enforce_on_create: false,
    } },
  ],
}

/** Validate the effective main rules and every contributing ruleset's bypass actors. */
export function checkProtection(effective, rulesets) {
  if (!Array.isArray(effective) || !Array.isArray(rulesets))
    throw new Error('Protection responses must be arrays.')
  const required = ['deletion', 'non_fast_forward', 'required_linear_history', 'pull_request', 'required_status_checks']
  for (const type of required) {
    if (!effective.some(rule => rule.type === type))
      throw new Error(`main lacks the ${type} rule.`)
  }
  for (const rule of effective) {
    const source = rulesets.find(set => set.id === rule.ruleset_id)
    if (!source || source.enforcement !== 'active')
      throw new Error(`Cannot verify active ruleset ${rule.ruleset_id}.`)
    if (!Array.isArray(source.bypass_actors))
      throw new Error(`Ruleset ${source.id} omits bypass actors.`)
    for (const actor of source.bypass_actors) {
      if (actor.actor_type !== 'RepositoryRole' || actor.actor_id !== 5 || actor.bypass_mode !== 'always')
        throw new Error(`Ruleset ${source.id} permits a bypass other than repository administrators.`)
    }
  }
  const reviews = effective.filter(rule => rule.type === 'pull_request')
  if (!reviews.some(({ parameters: p, ruleset_id: id }) => p?.required_approving_review_count >= 1
    && p.dismiss_stale_reviews_on_push === true && p.require_last_push_approval === true
    && rulesets.find(set => set.id === id)?.bypass_actors.some(actor => actor.actor_type === 'RepositoryRole' && actor.actor_id === 5))) {
    throw new Error('main requires one review, stale-review dismissal, latest-push approval, and administrator bypass.')
  }
  for (const context of ['Required CI', 'NOTICE']) {
    if (!effective.some(rule => rule.type === 'required_status_checks'
      && rule.parameters?.required_status_checks?.some(check => check.context === context && check.integration_id === 15368))) {
      throw new Error(`main lacks the ${context} check from GitHub Actions (15368).`)
    }
  }
}

export async function verifyRepository({ token = process.env.GH_TOKEN, repository = 'leapmux/leapmux', request = fetch } = {}) {
  if (repository !== 'leapmux/leapmux' || !token)
    throw new Error('Protection verification requires leapmux/leapmux and a read-only token.')
  const get = async (path) => {
    const response = await request(`https://api.github.com${path}`, { signal: AbortSignal.timeout(30000), headers: {
      'Authorization': `Bearer ${token}`,
      'Accept': 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28',
    } })
    if (!response.ok)
      throw new Error(`GitHub protection read failed: ${response.status} ${path}`)
    return response.json()
  }
  const effective = []
  for (let page = 1; ; page++) {
    const batch = await get(`/repos/${repository}/rules/branches/main?per_page=100&page=${page}`)
    if (!Array.isArray(batch))
      throw new Error('GitHub returned an invalid effective rule list.')
    effective.push(...batch)
    if (batch.length < 100)
      break
    if (page >= 100)
      throw new Error('Effective protection exceeds the supported rule count.')
  }
  const ids = [...new Set(effective.map(rule => rule.ruleset_id))]
  const rulesets = await Promise.all(ids.map(id => get(`/repos/${repository}/rulesets/${id}`)))
  checkProtection(effective, rulesets)
  return { effective, rulesets }
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
