import { describe, expect, it } from 'bun:test'
import { githubRequest, migratePullRequest, publishCapturedDetails, publishRecord } from './publish-renovate-details.mjs'
import { COMMENT_MARKER } from './renovate-pr-details.mjs'

const bot = 'leapmux-renovate[bot]'
const root = '/repos/leapmux/leapmux'
const template = '## Motivation\n\n- Update dependencies.\n\n## Modifications\n\n{{{table}}}\n\n## Result\n\n- Review and CI remain required.'
function record() {
  return { version: 1, repository: 'leapmux/leapmux', branchName: 'renovate/go-security', warnings: 'Artifact generation failed.', notes: 'Advisory details.\n'.repeat(4000), changelogs: 'Release note details.' }
}
function fixture(actor = bot) {
  const pr = { number: 546, user: { login: bot, type: 'Bot' }, base: { ref: 'main', repo: { full_name: 'leapmux/leapmux' } }, head: { ref: 'renovate/go-security', repo: { full_name: 'leapmux/leapmux' } }, body: `${template.replace('{{{table}}}', '- Runtime: 1 → 2')}\n\nLegacy advisory details.` }
  const comments = [{ id: 1, user: { login: 'human' }, body: `${COMMENT_MARKER}1 -->\nHuman content.` }]
  const calls = []
  let nextId = 2
  const api = async (method, path, body) => {
    calls.push({ method, path, body })
    const url = new URL(`https://api.github.com${path}`)
    const commentId = Number(url.pathname.match(/issues\/comments\/(\d+)$/)?.[1])
    if (method === 'GET' && url.pathname === `${root}/pulls`)
      return [structuredClone(pr)]
    if (method === 'GET' && url.pathname === `${root}/issues/546/comments`)
      return comments.slice((Number(url.searchParams.get('page')) - 1) * 100, Number(url.searchParams.get('page')) * 100).map(value => structuredClone(value))
    if (method === 'POST' && url.pathname === `${root}/issues/546/comments`) {
      const created = { id: nextId++, user: { login: actor }, body: body.body, html_url: `https://github.com/leapmux/leapmux/pull/546#issuecomment-${nextId}` }
      comments.push(created)
      return structuredClone(created)
    }
    if (method === 'GET' && commentId)
      return structuredClone(comments.find(comment => comment.id === commentId))
    if (method === 'PATCH' && commentId) {
      const comment = comments.find(comment => comment.id === commentId)
      comment.body = body.body
      return structuredClone(comment)
    }
    if (method === 'PATCH' && url.pathname === `${root}/pulls/546`) {
      pr.body = body.body
      return structuredClone(pr)
    }
    if (method === 'GET' && url.pathname === `${root}/pulls/546`)
      return structuredClone(pr)
    throw new Error(`Unexpected ${method} ${path}`)
  }
  return { pr, comments, calls, api }
}

describe('managed Renovate detail comments', () => {
  it('publishes complete details before cleaning the body and never edits human comments', async () => {
    const state = fixture()
    const data = record()
    await publishRecord(data, state.pr, template, { api: state.api })
    expect(state.pr.body).not.toContain('Legacy advisory details.')
    expect(state.pr.body).toContain('- Runtime: 1 → 2')
    expect([...state.pr.body.matchAll(/^## (.+)$/gm)].map(match => match[1])).toEqual(['Motivation', 'Modifications', 'Result'])
    expect(state.comments[0].body).toContain('Human content.')
    const managed = state.comments.filter(comment => comment.user.login === bot)
    expect(managed.length).toBeGreaterThan(1)
    expect(managed[0].body).toContain(data.warnings)
    expect(managed.at(-1).body).toContain(data.changelogs)
    expect(managed.map(comment => comment.body.match(/Advisory details\./g)?.length ?? 0).reduce((a, b) => a + b, 0)).toBe(4000)
    const bodyUpdate = state.calls.findIndex(call => call.method === 'PATCH' && call.path === `${root}/pulls/546`)
    expect(state.calls.slice(bodyUpdate).some(call => call.method === 'POST')).toBe(false)
    const posts = state.calls.filter(call => call.method === 'POST').length
    await publishRecord(data, state.pr, template, { api: state.api })
    expect(state.calls.filter(call => call.method === 'POST')).toHaveLength(posts)
    expect(state.comments[0].body).toContain('Human content.')
  })
  it('updates existing parts and retires surplus parts without deleting comments', async () => {
    const state = fixture()
    await publishRecord(record(), state.pr, template, { api: state.api })
    const count = state.comments.length
    await publishRecord({ ...record(), warnings: '', notes: 'Changed advisory.', changelogs: '' }, state.pr, template, { api: state.api })
    expect(state.comments).toHaveLength(count)
    expect(state.comments[1].body).toContain('Changed advisory.')
    expect(state.comments.at(-1).body).toContain('no longer needs')
    expect(state.calls.some(call => call.method === 'DELETE')).toBe(false)
  })
  it('preserves the original body after failed writes or inconsistent read-back', async () => {
    for (const failure of ['write', 'author', 'readback']) {
      const state = fixture()
      const original = state.pr.body
      const api = async (method, path, body) => {
        if (failure === 'write' && method === 'POST')
          throw new Error('Network failure')
        const result = await state.api(method, path, body)
        if (failure === 'author' && method === 'POST')
          result.user.login = 'unexpected-author'
        if (failure === 'readback' && method === 'GET' && path.includes('/issues/comments/'))
          result.body = 'Unexpected stored value'
        return result
      }
      await expect(publishRecord(record(), state.pr, template, { api })).rejects.toThrow()
      expect(state.pr.body).toBe(original)
    }
  })
  it('requires the correct bot, repository, branch, base, and PR identifier', async () => {
    for (const change of [
      pr => pr.user.login = 'human',
      pr => pr.user.type = 'User',
      pr => pr.base.ref = 'other',
      pr => pr.head.repo.full_name = 'other/repo',
      pr => pr.base.repo.full_name = 'other/repo',
      pr => pr.head.ref = 'renovate/other',
      pr => pr.number = 0,
    ]) {
      const state = fixture()
      change(state.pr)
      await expect(publishRecord(record(), state.pr, template, { api: state.api })).rejects.toThrow('expected Renovate PR')
      expect(state.calls).toEqual([])
    }
  })
  it('uses the newest capture per branch and skips proposals without an open PR', async () => {
    const state = fixture()
    await publishCapturedDetails([record(), { ...record(), notes: 'Newest advisory.' }], template, { api: state.api })
    expect(state.comments[1].body).toContain('Newest advisory.')
    expect(state.comments[1].body).not.toContain('Advisory details.')
    const result = await publishCapturedDetails([record()], template, { api: async () => [] })
    expect(result[0].skipped).toContain('No open PR')
    await expect(publishCapturedDetails([record()], template, { api: async () => [state.pr, state.pr] })).rejects.toThrow('More than one')
  })
  it('allows an explicit administrator migration without changing authorship', async () => {
    const state = fixture('trustin')
    await publishRecord({ ...record(), notes: 'Historical details.' }, state.pr, template, { api: state.api, actorLogin: 'trustin' })
    expect(state.comments[1].user.login).toBe('trustin')
    expect(state.pr.user.login).toBe(bot)
  })
  it('keeps migrated details intact when the migration is retried', async () => {
    const state = fixture('trustin')
    await migratePullRequest(state.pr, template, { api: state.api, actorLogin: 'trustin' })
    const stored = structuredClone(state.comments)
    const calls = state.calls.length
    const result = await migratePullRequest(state.pr, template, { api: state.api, actorLogin: 'trustin' })
    expect(result.skipped).toContain('already contains')
    expect(state.comments).toEqual(stored)
    expect(state.calls).toHaveLength(calls)
  })
  it('finds managed parts after pagination and rejects duplicate owned parts', async () => {
    const state = fixture()
    for (let id = 2; id <= 100; id++)
      state.comments.push({ id, user: { login: 'human' }, body: 'Human discussion.' })
    state.comments.push({ id: 999, user: { login: bot }, body: `${COMMENT_MARKER}1 -->\nOld details.` })
    await publishRecord({ ...record(), warnings: '', notes: 'Updated details.', changelogs: '' }, state.pr, template, { api: state.api })
    expect(state.calls.some(call => call.path.endsWith('page=2'))).toBe(true)
    expect(state.calls.some(call => call.method === 'POST')).toBe(false)
    expect(state.comments.at(-1).body).toContain('Updated details.')
    state.comments.push({ id: 1000, user: { login: bot }, body: `${COMMENT_MARKER}1 -->\nDuplicate.` })
    await expect(publishRecord(record(), state.pr, template, { api: state.api })).rejects.toThrow('duplicate')
  })
  it('retires an existing sparse part without overwriting unrelated comments', async () => {
    const state = fixture()
    state.comments.push({ id: 999, user: { login: bot }, body: `${COMMENT_MARKER}3 -->\nOld third part.` })
    await publishRecord({ ...record(), warnings: '', notes: 'Current details.', changelogs: '' }, state.pr, template, { api: state.api })
    expect(state.comments.find(comment => comment.id === 999).body).toContain('no longer needs')
    expect(state.comments[0].body).toContain('Human content.')
    expect(state.comments.some(comment => comment.body.includes('Current details.'))).toBe(true)
  })
  it('rejects unsupported API writes before invoking the CLI', async () => {
    for (const [method, path, body] of [['DELETE', `${root}/issues/comments/1`], ['POST', `${root}/actions/workflows/1/dispatches`, {}], ['PATCH', '/repos/other/repo/pulls/1', { body: 'x' }], ['PATCH', `${root}/pulls/1`, { state: 'closed' }], ['GET', `${root}/pulls/1`, { body: 'x' }]])
      await expect(githubRequest(method, path, body)).rejects.toThrow('only use')
  })
})
