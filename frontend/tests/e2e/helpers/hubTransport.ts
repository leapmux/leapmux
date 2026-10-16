/** The browser origin and cookie domain of the private test Hub. */
export const E2E_BROWSER_HOST = 'localhost'

/** The private test Hub binds this address. Node clients must use the same address family. */
export const E2E_HUB_HOST = '127.0.0.1'

/**
 * Connect Node clients to the private IPv4 listener.
 * macOS can connect an IPv6 socket to itself when its ephemeral port equals the destination port.
 * Node resolves localhost to ::1 first, where the IPv4 Hub has no listener.
 * A self-connected HTTP client then receives its own request as the response.
 * Secure URLs retain their hostname because TLS uses it to check the certificate.
 */
export function hubTransportUrl(url: string): string {
  const parsed = new URL(url)
  if (parsed.hostname !== E2E_BROWSER_HOST || (parsed.protocol !== 'http:' && parsed.protocol !== 'ws:'))
    return url
  parsed.hostname = E2E_HUB_HOST
  return parsed.href
}

/**
 * Send a Hub request from Node to its listener with the caller's options.
 * URL validation rejects asynchronously, as native fetch does.
 */
export async function fetchHub(url: string, init?: RequestInit): Promise<Response> {
  return fetch(hubTransportUrl(url), init)
}
