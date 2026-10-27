/** Read GitHub JSON with one authentication, API-version, and timeout policy. */
export async function readGithubJsonResponse(path, { token, request = fetch, description = 'GitHub read' }) {
  if (typeof token !== 'string' || token === '')
    throw new Error('A GitHub token is required.')
  if (typeof path !== 'string' || !path.startsWith('/') || path.startsWith('//'))
    throw new Error('A GitHub API path must start with one slash.')
  const response = await request(`https://api.github.com${path}`, { method: 'GET', signal: AbortSignal.timeout(30000), headers: {
    'Authorization': `Bearer ${token}`,
    'Accept': 'application/vnd.github+json',
    'X-GitHub-Api-Version': '2022-11-28',
  } })
  if (!response.ok)
    throw new Error(`${description} failed: ${response.status} ${path}`)
  return { data: await response.json(), headers: response.headers }
}
