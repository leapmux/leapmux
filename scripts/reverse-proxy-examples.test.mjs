import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'bun:test'
import {
  EXAMPLES_DIR,
  EXAMPLES_PAGE,
  installPath,
  listExamples,
  loadApacheSite,
  pinnedImage,
  prepareProxyFiles,
  PROXIES,
  readExample,
  requiredApacheModules,
  ROOT,
  TEST_IMAGE_KEYS,
  useCaddyLocalCertificates,
} from './reverse-proxy-examples.mjs'

const STOCK_HTTPD = [
  'Listen 80',
  'LoadModule mpm_event_module modules/mod_mpm_event.so',
  '#LoadModule ssl_module modules/mod_ssl.so',
  '#LoadModule http2_module modules/mod_http2.so',
  'LoadModule headers_module modules/mod_headers.so',
  '#LoadModule proxy_module modules/mod_proxy.so',
  '',
].join('\n')

describe('PROXIES', () => {
  it('lists every example file of the page bundle, and only files that exist', () => {
    const listed = PROXIES.flatMap(proxy => proxy.examples)
    expect({
      missing: listed.filter(name => !existsSync(join(EXAMPLES_DIR, name))),
      unused: listExamples().filter(name => !listed.includes(name)),
    }).toEqual({ missing: [], unused: [] })
  })

  it('gives each example file to one proxy only', () => {
    const listed = PROXIES.flatMap(proxy => proxy.examples)
    expect(listed.toSorted()).toEqual([
      ...new Set(listed),
    ].toSorted())
  })

  it('pins each test image in versions.env as an exact release that Renovate tracks', () => {
    const text = readFileSync(join(ROOT, 'versions.env'), 'utf8')
    const untracked = TEST_IMAGE_KEYS.filter((key) => {
      const match = new RegExp(`^# renovate: datasource=docker depName=(\\S+) versioning=docker\\n${key}=(\\S+):(\\d+\\.\\d+\\.\\d+(?:-alpine)?)$`, 'm').exec(text)
      return !match || match[2] !== match[1] || pinnedImage(key) !== `${match[1]}:${match[3]}`
    })
    expect(untracked).toEqual([])
  })

  it('refuses an image key that versions.env does not define', () => {
    expect(() => pinnedImage('NO_SUCH_IMAGE')).toThrow('versions.env does not define NO_SUCH_IMAGE.')
  })

  it('lists each proxy once', () => {
    const names = PROXIES.map(proxy => proxy.name)
    expect(names).toEqual([
      ...new Set(names),
    ])
  })

  it('mounts each certificate at a path that the proxy\'s own examples read', () => {
    const unread = PROXIES.flatMap((proxy) => {
      const examples = proxy.examples.map(name => readExample(name)).join('\n')
      return proxy.certificates
        .filter(certificate => !examples.includes(certificate.target))
        .map(certificate => `${proxy.name}: ${certificate.target}`)
    })
    expect(unread).toEqual([])
  })

  it('mounts the examples of one proxy at distinct paths', () => {
    for (const proxy of PROXIES) {
      const paths = proxy.examples.map(name => installPath(name, readExample(name)))
      expect(paths).toEqual([
        ...new Set(paths),
      ])
    }
  })
})

describe('the reverse proxy page', () => {
  const page = readFileSync(EXAMPLES_PAGE, 'utf8')

  it('renders every example file through the config-file shortcode', () => {
    expect(listExamples().filter(name => !page.includes(`{{< config-file file="${name}"`))).toEqual([])
  })

  it('enables with a2enmod exactly the Apache modules that the harness loads', () => {
    const modules = requiredApacheModules(readExample('apache.conf'))
    expect(page).toContain(`sudo a2enmod ${modules.join(' ')}\n`)
  })

  it('renders no file that the bundle lacks', () => {
    const rendered = [
      ...page.matchAll(/\{\{< config-file file="([^"]+)"/g),
    ].map(match => match[1])
    expect(rendered.filter(name => !listExamples().includes(name))).toEqual([])
  })
})

describe('installPath', () => {
  it('returns the absolute path on the first comment line', () => {
    expect(installPath('nginx.conf', '# /etc/nginx/conf.d/leapmux.conf\nserver {}\n')).toBe('/etc/nginx/conf.d/leapmux.conf')
  })

  it('rejects a file whose first line states no absolute path', () => {
    expect(() => installPath('nginx.conf', 'server {}\n')).toThrow('nginx.conf must start with a comment line')
    expect(() => installPath('nginx.conf', '# etc/nginx.conf\n')).toThrow('nginx.conf must start with a comment line')
  })

  it('accepts every example of the page bundle', () => {
    expect(listExamples().map(name => installPath(name, readExample(name))).filter(path => !path.startsWith('/'))).toEqual([])
  })
})

describe('requiredApacheModules', () => {
  it('lists the modules on the comment line', () => {
    expect(requiredApacheModules('# /x\n# Required modules: ssl http2  proxy\n')).toEqual(['ssl', 'http2', 'proxy'])
  })

  it('rejects a site that lists no modules', () => {
    expect(() => requiredApacheModules('# /x\n<VirtualHost *:443>\n')).toThrow('Required modules')
  })
})

describe('loadApacheSite', () => {
  const site = '# /etc/apache2/sites-available/leapmux.conf\n# Required modules: ssl http2 headers\n'

  it('loads each required module, listens on 443, and includes the site', () => {
    const config = loadApacheSite(STOCK_HTTPD, site, '/etc/apache2/sites-available/leapmux.conf')
    expect(config).toContain('\nLoadModule ssl_module modules/mod_ssl.so\n')
    expect(config).toContain('\nLoadModule http2_module modules/mod_http2.so\n')
    expect(config).toContain('\nLoadModule headers_module modules/mod_headers.so\n')
    // A module that the site does not list stays off.
    expect(config).toContain('\n#LoadModule proxy_module modules/mod_proxy.so\n')
    expect(config).toEndWith('\nListen 443\nInclude /etc/apache2/sites-available/leapmux.conf\n')
  })

  it('rejects a module that the stock configuration cannot load', () => {
    expect(() => loadApacheSite(STOCK_HTTPD, '# Required modules: proxy_http2\n', '/site.conf'))
      .toThrow('no LoadModule line for proxy_http2_module')
  })
})

describe('useCaddyLocalCertificates', () => {
  it('prepends a global options block that selects the local certificate authority', () => {
    expect(useCaddyLocalCertificates('# /etc/caddy/Caddyfile\nhub.example.com {\n}\n'))
      .toBe('{\n\tlocal_certs\n}\n\n# /etc/caddy/Caddyfile\nhub.example.com {\n}\n')
  })

  it('rejects a Caddyfile that has its own global options block', () => {
    expect(() => useCaddyLocalCertificates('# /etc/caddy/Caddyfile\n\n{\n\temail a@example.com\n}\n'))
      .toThrow('own global options block')
  })
})

describe('prepareProxyFiles', () => {
  const proxy = name => PROXIES.find(entry => entry.name === name)

  it('reads and prepares examples from the selected directory', () => {
    mkdirSync(join(ROOT, '.tmp'), { recursive: true })
    const directory = mkdtempSync(join(ROOT, '.tmp', 'reverse-proxy-examples-'))
    const source = readExample('nginx.conf').replaceAll('hub.example.com', 'variant.example.test')
    try {
      writeFileSync(join(directory, 'nginx.conf'), source)
      expect(readExample('nginx.conf', directory)).toBe(source)
      const [file] = prepareProxyFiles(proxy('nginx'), undefined, directory)
      expect(file.content).toBe(source)
      expect(file.target).toBe('/etc/nginx/conf.d/leapmux.conf')
    }
    finally {
      rmSync(directory, { recursive: true, force: true })
    }
  })

  it('mounts each example of a proxy at its install path, unchanged', () => {
    const files = prepareProxyFiles(proxy('traefik'))
    expect(files.map(file => file.target)).toEqual(['/etc/traefik/traefik.yml', '/etc/traefik/dynamic/leapmux.yml'])
    expect(files[0].content).toBe(readExample('traefik.yml'))
  })

  it('changes the Caddyfile to use local certificates', () => {
    const [file] = prepareProxyFiles(proxy('caddy'))
    expect(file.content).toStartWith('{\n\tlocal_certs\n}\n')
    expect(file.content).toEndWith(readExample('Caddyfile'))
  })

  it('adds the stock httpd.conf that loads the Apache site', () => {
    const stock = `${listModuleLines(requiredApacheModules(readExample('apache.conf')))}Listen 80\n`
    const files = prepareProxyFiles(proxy('apache'), stock)
    expect(files.map(file => file.target)).toEqual(['/etc/apache2/sites-available/leapmux.conf', '/usr/local/apache2/conf/httpd.conf'])
    expect(files[1].content).toEndWith('Include /etc/apache2/sites-available/leapmux.conf\n')
  })

  it('requires the stock configuration for a proxy that changes one', () => {
    expect(() => prepareProxyFiles(proxy('apache'))).toThrow('needs the content of /usr/local/apache2/conf/httpd.conf')
  })
})

function listModuleLines(modules) {
  return modules.map(module => `#LoadModule ${module}_module modules/mod_${module}.so\n`).join('')
}
