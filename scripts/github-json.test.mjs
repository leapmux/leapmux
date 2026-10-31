import { describe, expect, it } from 'bun:test'
import { nextGithubPage, readGithubJsonResponse } from './github-json.mjs'

describe('readGithubJsonResponse', () => {
  it('uses an explicit GET, repository path, authentication, API version, and deadline', async () => {
    const result = await readGithubJsonResponse('/repos/leapmux/leapmux/rules/branches/main', {
      token: 'fixture',
      request: async (url, options) => {
        expect(url).toBe('https://api.github.com/repos/leapmux/leapmux/rules/branches/main')
        expect(options.method).toBe('GET')
        expect(options.headers).toEqual({ 'Authorization': 'Bearer fixture', 'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' })
        expect(options.signal).toBeInstanceOf(AbortSignal)
        return Response.json({ rules: [] }, {
          headers: { link: '</next>; rel="next"' },
        })
      },
    })
    expect(result.data).toEqual({ rules: [] })
    expect(result.headers.get('link')).toBe('</next>; rel="next"')
  })
  it('rejects missing credentials and paths that could change the API origin before requesting', async () => {
    const request = () => {
      throw new Error('Unexpected request.')
    }
    for (const path of [
      'https://other.invalid',
      '//other.invalid',
      '@other.invalid',
      null,
    ])
      await expect(readGithubJsonResponse(path, { token: 'fixture', request })).rejects.toThrow('API path')
    await expect(readGithubJsonResponse('/repos/leapmux/leapmux', { token: '', request })).rejects.toThrow('token')
  })
  it('preserves HTTP context, malformed JSON, and transport failures', async () => {
    await expect(readGithubJsonResponse('/fixture', {
      token: 'fixture',
      description: 'Protection read',
      request: async () => new Response('', { status: 403 }),
    })).rejects.toThrow('Protection read failed: 403 /fixture')
    await expect(readGithubJsonResponse('/fixture', {
      token: 'fixture',
      request: async () => new Response('invalid'),
    })).rejects.toThrow()
    await expect(readGithubJsonResponse('/fixture', {
      token: 'fixture',
      request: async () => {
        throw new Error('transport failed')
      },
    })).rejects.toThrow('transport failed')
  })
})

describe('nextGithubPage', () => {
  const accept = url => url.pathname === '/items' && url.searchParams.has('after')
  it('returns the sorted path of the next link, and null without a next link', () => {
    expect(nextGithubPage('<https://api.github.com/items?z=1&after=c>; rel="next", <https://api.github.com/items?after=a>; rel="prev"', accept)).toBe('/items?after=c&z=1')
    expect(nextGithubPage('</items?after=c>; rel=next', accept)).toBe('/items?after=c')
    expect(nextGithubPage('<https://api.github.com/items?after=c>; rel="last next"', accept)).toBe('/items?after=c')
    expect(nextGithubPage('<https://api.github.com/items?after=a>; rel="prev"', accept)).toBeNull()
    for (const header of [null, undefined, ''])
      expect(nextGithubPage(header, accept), String(header)).toBeNull()
  })
  it('rejects a malformed header', () => {
    for (const header of ['invalid link header', '<https://api.github.com/items?after=c>', '<https://api.github.com/items?after=c>; next'])
      expect(() => nextGithubPage(header, accept), header).toThrow('Invalid GitHub pagination header.')
    expect(() => nextGithubPage('<http://[::1>; rel="next"', accept)).toThrow('Invalid GitHub pagination URL.')
  })
  it('rejects a link that leaves the API, carries credentials or a fragment, repeats next, or that the caller refuses', () => {
    for (const header of [
      '<https://other.invalid/items?after=c>; rel="next"',
      '<http://api.github.com/items?after=c>; rel="next"',
      '<https://user:secret@api.github.com/items?after=c>; rel="next"',
      '<https://api.github.com/items?after=c#fragment>; rel="next"',
      '<https://api.github.com/other?after=c>; rel="next"',
      '<https://api.github.com/items>; rel="next"',
      '<https://api.github.com/items?after=c>; rel="next", <https://api.github.com/items?after=d>; rel="next"',
    ])
      expect(() => nextGithubPage(header, accept), header).toThrow('Unsafe or ambiguous GitHub pagination link.')
  })
})
