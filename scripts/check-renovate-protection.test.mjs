import { describe, expect, it } from 'bun:test'
import { checkProtection, createProtectionSnapshot, RENOVATE_MAIN_RULESET, verifyRepository } from './check-renovate-protection.mjs'

function fixture() {
  const rulesets = [{ id: 1, enforcement: 'active', bypass_actors: [] }, {
    id: 2,
    enforcement: 'active',
    bypass_actors: [{ actor_type: 'RepositoryRole', actor_id: 5, bypass_mode: 'always' }],
  }]
  const effective = ['deletion', 'non_fast_forward', 'required_linear_history'].map(type => ({ type, ruleset_id: 1 }))
  effective.push({ type: 'pull_request', ruleset_id: 2, parameters: {
    required_approving_review_count: 1,
    dismiss_stale_reviews_on_push: true,
    require_last_push_approval: true,
  } }, { type: 'required_status_checks', ruleset_id: 2, parameters: {
    strict_required_status_checks_policy: true,
    required_status_checks: ['Required CI', 'NOTICE'].map(context => ({ context, integration_id: 15368 })),
  } })
  return { effective, rulesets }
}

function snapshotFixture() {
  const data = fixture()
  for (const rule of data.rulesets) {
    rule.name = `Protection ${rule.id}`
    rule.updated_at = '2026-10-28T00:01:00.456Z'
    rule.created_at = '2026-10-28T00:00:00.123Z'
    rule.conditions = { ref_name: { include: ['refs/heads/main'], exclude: [] } }
    rule.rules = structuredClone(data.effective.filter(item => item.ruleset_id === rule.id))
  }
  return data
}

function protectionRequest({ effective, rulesets }, hideActors = true) {
  return async (url, options) => {
    expect(options.method).toBe('GET')
    expect(options.cache).toBe('no-store')
    expect(options.headers['Cache-Control']).toBe('no-cache')
    if (new URL(url).pathname.endsWith('/main'))
      return Response.json(effective)
    const data = structuredClone(rulesets.find(set => url.endsWith(`/${set.id}`)))
    if (hideActors)
      delete data.bypass_actors
    return Response.json(data)
  }
}

describe('checkProtection', () => {
  it('rejects checks that permit stale base revisions', () => {
    for (const value of [false, undefined]) {
      const { effective, rulesets } = fixture()
      effective[4].parameters.strict_required_status_checks_policy = value
      expect(() => checkProtection(effective, rulesets)).toThrow('strict')
    }
  })
  it('requires administrator bypass on every review or check rule that can block a release push', () => {
    for (const type of ['pull_request', 'required_status_checks']) {
      const { effective, rulesets } = fixture()
      rulesets.push({ id: 3, enforcement: 'active', bypass_actors: [] })
      effective.push({ ...structuredClone(effective[type === 'pull_request' ? 3 : 4]), ruleset_id: 3 })
      expect(() => checkProtection(effective, rulesets), type).toThrow('administrator')
    }
    const { effective, rulesets } = fixture()
    rulesets.push({ id: 3, enforcement: 'active', bypass_actors: [] })
    effective[4].ruleset_id = 3
    expect(() => checkProtection(effective, rulesets)).toThrow('administrator')
    rulesets[2].bypass_actors.push({ actor_type: 'RepositoryRole', actor_id: 5, bypass_mode: 'always' })
    expect(() => checkProtection(effective, rulesets)).not.toThrow()
  })
  it('accepts separate effective rulesets with only an administrator bypass', () => {
    const { effective, rulesets } = fixture()
    expect(() => checkProtection(effective, rulesets)).not.toThrow()
  })
  it('accepts the emitted ruleset alongside existing protection and stricter reviews', () => {
    const baseline = fixture()
    const emitted = { ...structuredClone(RENOVATE_MAIN_RULESET), id: 3 }
    const effective = baseline.effective.slice(0, 3).concat(emitted.rules.map(rule => ({ ...rule, ruleset_id: 3 })))
    expect(() => checkProtection(effective, [baseline.rulesets[0], emitted])).not.toThrow()
    effective.find(rule => rule.type === 'pull_request').parameters.required_approving_review_count = 2
    expect(() => checkProtection(effective, [baseline.rulesets[0], emitted])).not.toThrow()
  })
  it('rejects each missing protection and unverifiable ruleset', () => {
    for (const index of [0, 1, 2, 3, 4]) {
      const { effective, rulesets } = fixture()
      effective.splice(index, 1)
      expect(() => checkProtection(effective, rulesets)).toThrow()
    }
    for (const rulesets of [[], [{ id: 1, enforcement: 'disabled', bypass_actors: [] }]])
      expect(() => checkProtection(fixture().effective, rulesets)).toThrow()
  })
  it('rejects weakened reviews, spoofed checks, and app or organization bypasses', () => {
    for (const [key, value] of [['required_approving_review_count', 0], ['dismiss_stale_reviews_on_push', false], ['require_last_push_approval', false]]) {
      const { effective, rulesets } = fixture()
      effective[3].parameters[key] = value
      expect(() => checkProtection(effective, rulesets)).toThrow()
    }
    for (const integration_id of [null, 0, 99]) {
      const { effective, rulesets } = fixture()
      effective[4].parameters.required_status_checks[0].integration_id = integration_id
      expect(() => checkProtection(effective, rulesets)).toThrow()
    }
    for (const actor of [{ actor_type: 'Integration', actor_id: 1, bypass_mode: 'always' }, { actor_type: 'OrganizationAdmin', actor_id: 1, bypass_mode: 'always' }, { actor_type: 'RepositoryRole', actor_id: 4, bypass_mode: 'always' }]) {
      const { effective, rulesets } = fixture()
      rulesets[0].bypass_actors.push(actor)
      expect(() => checkProtection(effective, rulesets)).toThrow()
    }
  })
  it('reads all effective sources and refuses failed API reads without writes', async () => {
    const { effective, rulesets } = fixture()
    const paths = []
    await verifyRepository({ token: 'fixture', request: async (url, options) => {
      expect(options.method).toBe('GET')
      paths.push(new URL(url).pathname)
      return Response.json(new URL(url).pathname.endsWith('/main') ? effective : rulesets.find(set => url.endsWith(`/${set.id}`)))
    } })
    expect(paths).toEqual(['/repos/leapmux/leapmux/rules/branches/main', '/repos/leapmux/leapmux/rulesets/1', '/repos/leapmux/leapmux/rulesets/2'])
    await expect(verifyRepository({ token: 'fixture', request: async () => new Response('', { status: 403 }) })).rejects.toThrow('403')
  })
  it('verifies hidden bypass actors from the administrator snapshot against fresh metadata', async () => {
    const { effective, rulesets } = fixture()
    for (const ruleset of rulesets) {
      ruleset.created_at = '2026-10-28T09:00:00.123+09:00'
      ruleset.updated_at = '2026-10-28T09:01:00.456+09:00'
    }
    const snapshot = { version: 1, repository: 'leapmux/leapmux', ref: 'refs/heads/main', rulesets }
    const request = async (url, options) => {
      expect(options.method).toBe('GET')
      if (new URL(url).pathname.endsWith('/main'))
        return Response.json(effective)
      const live = structuredClone(rulesets.find(set => url.endsWith(`/${set.id}`)))
      delete live.bypass_actors
      live.created_at = '2026-10-28T00:00:00.123Z'
      live.updated_at = '2026-10-28T00:01:00.456Z'
      return Response.json(live)
    }
    await expect(verifyRepository({ token: 'fixture', snapshot: JSON.stringify(snapshot), request })).resolves.toBeDefined()
  })
})

describe('administrator protection snapshot', () => {
  it('captures validated actors without recording viewer-specific access', () => {
    const data = snapshotFixture()
    data.rulesets[0].current_user_can_bypass = 'always'
    const snapshot = createProtectionSnapshot(data.effective, data.rulesets)
    expect(snapshot.version).toBe(1)
    expect(snapshot.repository).toBe('leapmux/leapmux')
    expect(snapshot.ref).toBe('refs/heads/main')
    expect(snapshot.rulesets[0]).not.toHaveProperty('current_user_can_bypass')
    expect(snapshot.rulesets[1].bypass_actors).toEqual([{ actor_type: 'RepositoryRole', actor_id: 5, bypass_mode: 'always' }])
    data.rulesets[1].bypass_actors[0].actor_id = 4
    expect(snapshot.rulesets[1].bypass_actors[0].actor_id).toBe(5)
    expect(() => createProtectionSnapshot(data.effective, data.rulesets)).toThrow('bypass')
  })
  it('refuses capture without administrator visibility, valid revisions, and exact sources', () => {
    for (const change of [
      data => delete data.rulesets[0].bypass_actors,
      data => delete data.rulesets[0].updated_at,
      data => data.rulesets.push(structuredClone(data.rulesets[0])),
      data => data.rulesets.pop(),
      data => data.rulesets.push({ ...data.rulesets[0], id: 3 }),
    ]) {
      const data = snapshotFixture()
      change(data)
      expect(() => createProtectionSnapshot(data.effective, data.rulesets)).toThrow()
    }
  })
  it('rejects missing, malformed, foreign, and unsupported snapshots', async () => {
    const data = snapshotFixture()
    const saved = createProtectionSnapshot(data.effective, data.rulesets)
    for (const snapshot of [undefined, '', 'invalid', 'null', '[]', '{}', { ...saved, version: 2 }, { ...saved, repository: 'other/repo' }, { ...saved, ref: 'refs/heads/other' }, { ...saved, rulesets: [] }, { ...saved, rulesets: null }])
      await expect(verifyRepository({ token: 'fixture', snapshot, request: protectionRequest(data) })).rejects.toThrow()
  })
  it('rejects revision and public metadata changes even when the visible rules remain sufficient', async () => {
    for (const change of [
      rule => rule.updated_at = '2026-10-28T00:01:00.457Z',
      rule => rule.name = 'Renamed protection',
      rule => rule.enforcement = 'disabled',
      rule => rule.conditions.ref_name.include.push('refs/heads/other'),
      rule => rule.rules.push({ type: 'creation' }),
      rule => rule.new_api_field = true,
    ]) {
      const data = snapshotFixture()
      const snapshot = createProtectionSnapshot(data.effective, data.rulesets)
      change(data.rulesets[0])
      await expect(verifyRepository({ token: 'fixture', snapshot, request: protectionRequest(data) })).rejects.toThrow('changed')
    }
  })
  it('rejects unavailable or invalid revision timestamps and invalid identifiers', async () => {
    for (const value of [undefined, null, '', 0, 'not a timestamp', '2026-10-28']) {
      const data = snapshotFixture()
      const snapshot = createProtectionSnapshot(data.effective, data.rulesets)
      data.rulesets[0].updated_at = value
      await expect(verifyRepository({ token: 'fixture', snapshot, request: protectionRequest(data) })).rejects.toThrow('timestamp')
    }
    for (const id of [undefined, null, 0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1, '1']) {
      const data = snapshotFixture()
      data.effective[0].ruleset_id = id
      await expect(verifyRepository({ token: 'fixture', request: protectionRequest(data) })).rejects.toThrow('source ID')
    }
  })
  it('rejects new, removed, duplicate, and mismatched ruleset sources', async () => {
    for (const change of [
      (data) => {
        data.rulesets.push({ ...structuredClone(data.rulesets[0]), id: 3 })
        data.effective.push({ type: 'creation', ruleset_id: 3 })
      },
      (data) => {
        data.rulesets.pop()
        data.effective = data.effective.filter(rule => rule.ruleset_id !== 2)
      },
    ]) {
      const data = snapshotFixture()
      const snapshot = createProtectionSnapshot(data.effective, data.rulesets)
      change(data)
      await expect(verifyRepository({ token: 'fixture', snapshot, request: protectionRequest(data) })).rejects.toThrow('exactly')
    }
    const data = snapshotFixture()
    const snapshot = createProtectionSnapshot(data.effective, data.rulesets)
    snapshot.rulesets.push(structuredClone(snapshot.rulesets[0]))
    await expect(verifyRepository({ token: 'fixture', snapshot, request: protectionRequest(data) })).rejects.toThrow('exactly')
  })
  it('does not trust unsafe snapshot actors or replace visible contradictory actors', async () => {
    for (const bypass_actors of [undefined, null, [], [{ actor_type: 'Integration', actor_id: 12, bypass_mode: 'always' }], [{ actor_type: 'RepositoryRole', actor_id: 4, bypass_mode: 'always' }]]) {
      const data = snapshotFixture()
      const snapshot = createProtectionSnapshot(data.effective, data.rulesets)
      snapshot.rulesets[1].bypass_actors = bypass_actors
      await expect(verifyRepository({ token: 'fixture', snapshot, request: protectionRequest(data) })).rejects.toThrow()
    }
    const data = snapshotFixture()
    const snapshot = createProtectionSnapshot(data.effective, data.rulesets)
    data.rulesets[1].bypass_actors = [{ actor_type: 'Integration', actor_id: 12, bypass_mode: 'always' }]
    await expect(verifyRepository({ token: 'fixture', snapshot, request: protectionRequest(data, false) })).rejects.toThrow('differ')
  })
  it('still rejects weakened effective rules and failed API reads with a valid snapshot', async () => {
    const data = snapshotFixture()
    const snapshot = createProtectionSnapshot(data.effective, data.rulesets)
    data.effective[4].parameters.required_status_checks[0].integration_id = 99
    await expect(verifyRepository({ token: 'fixture', snapshot, request: protectionRequest(data) })).rejects.toThrow('strict')
    await expect(verifyRepository({ token: 'fixture', snapshot, request: async () => new Response('', { status: 403 }) })).rejects.toThrow('403')
  })
})
