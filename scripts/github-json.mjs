/** Read GitHub JSON with one authentication, API-version, and timeout policy. */
export async function readGithubJsonResponse(path, { token, request = fetch, description = 'GitHub read' }) {
  if (typeof token !== 'string' || token === '')
    throw new Error('A GitHub token is required.')
  if (typeof path !== 'string' || !path.startsWith('/') || path.startsWith('//'))
    throw new Error('A GitHub API path must start with one slash.')
  const response = await request(`https://api.github.com${path}`, {
    method: 'GET',
    signal: AbortSignal.timeout(30000),
    headers: {
      'Authorization': `Bearer ${token}`,
      'Accept': 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28',
    },
  })
  if (!response.ok)
    throw new Error(`${description} failed: ${response.status} ${path}`)
  return { data: await response.json(), headers: response.headers }
}

/**
 * Return the API path of the `next` link of a GitHub Link header, or null when no page
 * follows. `accept(url)` approves the parsed URL of that link. Fail on a malformed header,
 * on a second `next` link, and on a link that leaves api.github.com or that `accept` rejects.
 */
export function nextGithubPage(header, accept) {
  if (!header)
    return null
  let next = null
  for (const part of header.split(/,\s*(?=<)/)) {
    const entry = /^\s*<([^>]+)>(.*)$/.exec(part)
    const relation = entry && /;\s*rel\s*=\s*(?:"([^"]*)"|([^;\s]+))/i.exec(entry[2])
    if (!relation)
      throw new Error('Invalid GitHub pagination header.')
    if (!(relation[1] ?? relation[2]).split(/\s+/).includes('next'))
      continue
    let url
    try {
      url = new URL(entry[1], 'https://api.github.com')
    }
    catch {
      throw new Error('Invalid GitHub pagination URL.')
    }
    if (next || url.origin !== 'https://api.github.com' || url.username || url.password || url.hash || !accept(url))
      throw new Error('Unsafe or ambiguous GitHub pagination link.')
    url.searchParams.sort()
    next = `${url.pathname}${url.search}`
  }
  return next
}
