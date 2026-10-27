import { describe, expect, it } from 'bun:test'
import { readGithubJson } from './github-json.mjs'

describe('readGithubJson', () => {
  it('uses an explicit GET, repository path, authentication, API version, and deadline', async () => {
    const result = await readGithubJson('/repos/leapmux/leapmux/rules/branches/main', { token: 'fixture', request: async (url, options) => {
      expect(url).toBe('https://api.github.com/repos/leapmux/leapmux/rules/branches/main')
      expect(options.method).toBe('GET')
      expect(options.headers).toEqual({ 'Authorization': 'Bearer fixture', 'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' })
      expect(options.signal).toBeInstanceOf(AbortSignal)
      return Response.json({ rules: [] })
    } })
    expect(result).toEqual({ rules: [] })
  })
  it('rejects missing credentials and paths that could change the API origin before requesting', async () => {
    const request = () => {
      throw new Error('Unexpected request.')
    }
    for (const path of ['https://other.invalid', '//other.invalid', '@other.invalid', null])
      await expect(readGithubJson(path, { token: 'fixture', request })).rejects.toThrow('API path')
    await expect(readGithubJson('/repos/leapmux/leapmux', { token: '', request })).rejects.toThrow('token')
  })
  it('preserves HTTP context, malformed JSON, and transport failures', async () => {
    await expect(readGithubJson('/fixture', { token: 'fixture', description: 'Protection read', request: async () => new Response('', { status: 403 }) })).rejects.toThrow('Protection read failed: 403 /fixture')
    await expect(readGithubJson('/fixture', { token: 'fixture', request: async () => new Response('invalid') })).rejects.toThrow()
    await expect(readGithubJson('/fixture', { token: 'fixture', request: async () => {
      throw new Error('transport failed')
    } })).rejects.toThrow('transport failed')
  })
})
