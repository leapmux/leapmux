// The reverse proxy examples of the documentation, and what the test harness needs to run each one.
//
// The documentation page site/content/docs/admin/reverse-proxy/ renders each example file with the config-file
// shortcode, and scripts/test-reverse-proxies.mjs mounts the same file into the proxy. The two therefore cannot drift.
import { readdirSync, readFileSync } from 'node:fs'
import { basename, join, resolve } from 'node:path'
import { parseVersions } from './sync-versions.mjs'

export const ROOT = resolve(import.meta.dirname, '..')
export const EXAMPLES_DIR = join(ROOT, 'site', 'content', 'docs', 'admin', 'reverse-proxy')
export const EXAMPLES_PAGE = join(EXAMPLES_DIR, 'index.md')

/** The public host name that every example serves. The harness gives the Hub container this network alias. */
export const PUBLIC_HOST = 'hub.example.com'

const LETS_ENCRYPT = `/etc/letsencrypt/live/${PUBLIC_HOST}`

/**
 * One entry for each proxy that the documentation covers.
 *
 * - `imageKey` is the versions.env key that pins the proxy's image, so that Renovate keeps it current.
 * - `examples` lists the example files of the page bundle. Each file states its install path on its first line, and
 *   the harness mounts the file there.
 * - `certificates` lists where the examples read the certificate. `chain` is the server certificate, `key` is its
 *   private key, and `combined` is both in one file.
 * - `renderExample` changes an example only where the test network differs from a real host.
 * - `command` replaces the image's command when the image does not read the documented path by default.
 * - `generatesCertificate` states that the proxy issues its own certificate, as Caddy does.
 * - `stockConfig` states a configuration file of the image that the harness must change to load the example.
 */
export const PROXIES = [
  {
    name: 'nginx',
    imageKey: 'NGINX_IMAGE',
    examples: ['nginx.conf'],
    certificates: [
      { kind: 'chain', target: `${LETS_ENCRYPT}/fullchain.pem` },
      { kind: 'key', target: `${LETS_ENCRYPT}/privkey.pem` },
    ],
  },
  {
    name: 'caddy',
    imageKey: 'CADDY_IMAGE',
    examples: ['Caddyfile'],
    renderExample: useCaddyLocalCertificates,
    certificates: [],
    generatesCertificate: { rootCertificate: '/data/caddy/pki/authorities/local/root.crt' },
  },
  {
    name: 'traefik',
    imageKey: 'TRAEFIK_IMAGE',
    examples: ['traefik.yml', 'traefik-dynamic.yml'],
    certificates: [
      { kind: 'chain', target: `/etc/traefik/certs/${PUBLIC_HOST}.crt` },
      { kind: 'key', target: `/etc/traefik/certs/${PUBLIC_HOST}.key` },
    ],
  },
  {
    name: 'haproxy',
    imageKey: 'HAPROXY_IMAGE',
    examples: ['haproxy.cfg'],
    certificates: [
      { kind: 'combined', target: `/etc/haproxy/certs/${PUBLIC_HOST}.pem` },
    ],
    command: ['haproxy', '-f', '/etc/haproxy/haproxy.cfg'],
  },
  {
    name: 'apache',
    imageKey: 'HTTPD_IMAGE',
    examples: ['apache.conf'],
    certificates: [
      { kind: 'chain', target: `${LETS_ENCRYPT}/fullchain.pem` },
      { kind: 'key', target: `${LETS_ENCRYPT}/privkey.pem` },
    ],
    stockConfig: { path: '/usr/local/apache2/conf/httpd.conf', render: loadApacheSite },
  },
]

/** The versions.env key of the curl image that plays the browser and the HTTP client. */
export const CLIENT_IMAGE_KEY = 'CURL_IMAGE'

/**
 * Every versions.env key of an image that only the harness runs. Renovate updates these as one group, and their
 * update is no toolchain change: scripts/renovate-post-upgrade.mjs imports this list.
 */
export const TEST_IMAGE_KEYS = Object.freeze([
  ...PROXIES.map(proxy => proxy.imageKey),
  CLIENT_IMAGE_KEY,
])

/** Return the image that versions.env pins under key, and fail for a key that it does not define. */
export function pinnedImage(key) {
  const image = parseVersions(readFileSync(join(ROOT, 'versions.env'), 'utf8')).get(key)
  if (!image)
    throw new Error(`versions.env does not define ${key}.`)
  return image
}

/** Read an example from directory, using the documentation page bundle by default. */
export function readExample(name, directory = EXAMPLES_DIR) {
  return readFileSync(join(directory, name), 'utf8')
}

/** List the example files of the page bundle: every file except the page itself. */
export function listExamples() {
  return readdirSync(EXAMPLES_DIR).filter(name => name !== 'index.md').sort()
}

/**
 * Return the install path that an example states on its first line, as `# /etc/nginx/conf.d/leapmux.conf`.
 * The harness mounts the example at that path, so the path that a reader copies is the path that the test uses.
 */
export function installPath(name, source) {
  const match = /^# (\/\S+)\n/.exec(source)
  if (!match)
    throw new Error(`${name} must start with a comment line that states its absolute install path, such as "# /etc/proxy/proxy.conf".`)
  return match[1]
}

/**
 * Return the Apache modules that the example lists on its "Required modules:" comment line.
 * The harness loads exactly these modules, so the list that a reader follows is the list that the test proves.
 */
export function requiredApacheModules(source) {
  const match = /^# Required modules: ([a-z0-9_ ]+)$/m.exec(source)
  if (!match)
    throw new Error('apache.conf must list its modules on a "# Required modules: ..." comment line.')
  return match[1].trim().split(/ +/)
}

/**
 * Change the stock httpd.conf of the httpd image to load the modules that the site requires, to listen on port 443,
 * and to include the site at its install path. A Debian or Ubuntu reader does the same with a2enmod and a2ensite.
 */
export function loadApacheSite(stock, site, sitePath) {
  let config = stock
  for (const module of requiredApacheModules(site)) {
    const line = new RegExp(`^#?(LoadModule ${module}_module modules/mod_${module}\\.so)$`, 'm')
    if (!line.test(config))
      throw new Error(`The stock httpd.conf has no LoadModule line for ${module}_module.`)
    config = config.replace(line, '$1')
  }
  return `${config}\nListen 443\nInclude ${sitePath}\n`
}

/**
 * Make a Caddyfile issue its certificate from Caddy's local certificate authority, because the test network has no
 * ACME server. The global options block must come first in a Caddyfile, so the example must not have its own.
 */
export function useCaddyLocalCertificates(source) {
  const firstDirective = source.split('\n').find(line => line.trim() !== '' && !line.trimStart().startsWith('#'))
  if (firstDirective?.trim() === '{')
    throw new Error('The Caddyfile example has its own global options block, so the harness cannot prepend local_certs.')
  return `{\n\tlocal_certs\n}\n\n${source}`
}

/**
 * Prepare the files that the harness mounts for one proxy: each example at its install path, after any change.
 * stockConfig is the content of proxy.stockConfig.path in the proxy's image, for a proxy that states one.
 * directory selects an alternate copy of the example files.
 */
export function prepareProxyFiles(proxy, stockConfig, directory = EXAMPLES_DIR) {
  const files = proxy.examples.map((name) => {
    const source = readExample(name, directory)
    const target = installPath(name, source)
    const content = proxy.renderExample
      ? proxy.renderExample(source)
      : source
    return { name, target, content }
  })
  if (proxy.stockConfig) {
    if (typeof stockConfig !== 'string')
      throw new TypeError(`${proxy.name} needs the content of ${proxy.stockConfig.path} from its image.`)
    const [site] = files
    files.push({
      name: basename(proxy.stockConfig.path),
      target: proxy.stockConfig.path,
      content: proxy.stockConfig.render(stockConfig, site.content, site.target),
    })
  }
  return files
}
