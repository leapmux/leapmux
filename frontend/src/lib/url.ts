const WWW_PREFIX_RE = /^www\./

/**
 * Best-effort hostname extraction. Returns the URL hostname with a leading
 * `www.` stripped, or the original string when the URL fails to parse.
 */
export function extractDomain(url: string): string {
  try {
    return new URL(url).hostname.replace(WWW_PREFIX_RE, '')
  }
  catch {
    return url
  }
}
