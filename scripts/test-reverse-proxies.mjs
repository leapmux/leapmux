// Verify every reverse proxy example of the documentation against a built LeapMux image.
//
// Usage: node scripts/test-reverse-proxies.mjs [--stability-seconds=N] [--keep-on-failure] [--examples-dir=<path>] <image> [proxy...]
//
// --keep-on-failure leaves the containers of a failed proxy running, for inspection. Remove them with
// `docker rm -f` and the network with `docker network rm`; the harness prints both names.
//
// For each proxy, the harness starts a private Docker network with these containers:
// - A Hub that listens on 127.0.0.1:4327 only, so every request must pass through the proxy.
// - The proxy, in the Hub's network namespace, with the documented example mounted at its install path. Its
//   upstream 127.0.0.1:4327 is therefore the Hub, exactly as on a single host.
// - A curl client and a Worker that reach the proxy as https://hub.example.com, through a test certificate authority.
//
// It then checks what the documentation promises:
// - Port 80 redirects to HTTPS.
// - The web app loads over HTTP/2.
// - The Hub records the client's address from X-Forwarded-For, and sees HTTPS through X-Forwarded-Proto.
// - The web app's event WebSocket upgrades and stays open for the stability window.
// - A Worker registers, connects, and stays connected for the stability window.
// - More than a mebibyte crosses the Worker's stream, before and after the window.
import assert from 'node:assert/strict'
import { Buffer } from 'node:buffer'
import { spawn } from 'node:child_process'
import { createHash, randomUUID } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { setTimeout as sleep } from 'node:timers/promises'
import { CLIENT_IMAGE_KEY, EXAMPLES_DIR, pinnedImage, prepareProxyFiles, PROXIES, PUBLIC_HOST, ROOT } from './reverse-proxy-examples.mjs'

// versions.env pins every image, so that Renovate keeps each one current. The certificates come from the
// container runtime's own Alpine image.
const CURL_IMAGE = pinnedImage(CLIENT_IMAGE_KEY)
const OPENSSL_IMAGE = pinnedImage('ALPINE_IMAGE')
const HUB_URL = `https://${PUBLIC_HOST}`
const ADMIN_USERNAME = 'proxy-admin'
const TRUSTED_PROXY_RANGES = '["127.0.0.1","::1"]'
const READY_DEADLINE_MS = 60_000
// Exceeds nginx's default 1 MiB body limit so a baseline HTTP/2 Worker stream fails without its override.
// The same transfer checks framing and delivery on the WebSocket stream.
const TRANSFER_BYTES = 3 * 1024 * 1024
const DEFAULT_STABILITY_SECONDS = 200
const wire = JSON.parse(readFileSync(join(ROOT, 'contracts', 'wire.json'), 'utf8'))
// An interrupt ends every wait at once, so that cleanup removes the containers without waiting out a window.
const interruption = new AbortController()

/** Run a command and return its exit status and output, whatever the status. */
function run(command, args, { input, timeoutMs = 120_000 } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      stdio: ['pipe', 'pipe', 'pipe'],
    })
    const stdout = []
    const stderr = []
    const timer = setTimeout(() => child.kill('SIGKILL'), timeoutMs)
    child.stdout.on('data', chunk => stdout.push(chunk))
    child.stderr.on('data', chunk => stderr.push(chunk))
    child.on('error', (error) => {
      clearTimeout(timer)
      reject(error)
    })
    child.on('close', (status, signal) => {
      clearTimeout(timer)
      resolve({
        status: status ?? (signal ? 128 : 1),
        stdout: Buffer.concat(stdout).toString('utf8'),
        stderr: Buffer.concat(stderr).toString('utf8'),
      })
    })
    child.stdin.end(input ?? '')
  })
}

/** Run a docker command, and fail with its output unless it succeeds. */
async function docker(args, options) {
  const result = await run('docker', args, options)
  if (result.status !== 0)
    throw new Error(`docker ${args.join(' ')} failed (${result.status}):\n${result.stdout}${result.stderr}`)
  return result.stdout.trim()
}

/** Poll check until it returns a value other than undefined, or fail at the deadline with the last error. */
async function poll(description, check, deadlineMs = READY_DEADLINE_MS) {
  const deadline = Date.now() + deadlineMs
  let lastError
  while (Date.now() < deadline) {
    try {
      const value = await check()
      if (value !== undefined)
        return value
    }
    catch (error) {
      lastError = error
    }
    // The containers start independently of this process. Their readiness is external state, so poll it.
    await sleep(500, undefined, { signal: interruption.signal })
  }
  throw new Error(`${description} did not happen within ${deadlineMs / 1000} seconds${lastError
    ? `: ${lastError.message}`
    : ''}`)
}

/** Create the test certificate authority and a server certificate for the public host. */
async function createCertificates(directory) {
  const script = [
    'set -eu',
    'apk add --no-cache openssl >/dev/null',
    'cd /out',
    'openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=LeapMux reverse proxy test CA" -keyout ca.key -out ca.pem',
    `openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=${PUBLIC_HOST}" -keyout server.key -out server.csr`,
    `printf "subjectAltName=DNS:${PUBLIC_HOST}\\nextendedKeyUsage=serverAuth\\nbasicConstraints=CA:FALSE\\n" > server.ext`,
    'openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 2 -extfile server.ext -out server.pem',
    'cat server.pem server.key > combined.pem',
    // The proxies read these files as an unprivileged user. They protect nothing outside this test.
    'chmod 0644 ./*',
  ].join('\n')
  // Docker creates a missing bind-mount source as root, which a rootless or remote engine cannot do.
  mkdirSync(directory, { recursive: true })
  await docker([
    'run',
    '--rm',
    '-v',
    `${directory}:/out`,
    OPENSSL_IMAGE,
    'sh',
    '-c',
    script,
  ])
  return {
    chain: join(directory, 'server.pem'),
    key: join(directory, 'server.key'),
    combined: join(directory, 'combined.pem'),
    authority: join(directory, 'ca.pem'),
  }
}

const USAGE = 'Usage: test-reverse-proxies.mjs [--stability-seconds=N] [--keep-on-failure] [--examples-dir=<path>] <image> [proxy...]'

function parseArguments(argv) {
  let stabilitySeconds = DEFAULT_STABILITY_SECONDS
  let keepOnFailure = false
  let examplesDirectory = EXAMPLES_DIR
  const positional = []
  for (const argument of argv) {
    const match = /^--stability-seconds=(\d+)$/.exec(argument)
    if (match) {
      stabilitySeconds = Number(match[1])
    }
    else if (argument === '--keep-on-failure') {
      keepOnFailure = true
    }
    else if (argument.startsWith('--examples-dir=')) {
      const directory = argument.slice('--examples-dir='.length)
      assert(directory, `--examples-dir requires a path. ${USAGE}`)
      examplesDirectory = resolve(directory)
    }
    else if (argument.startsWith('-')) {
      throw new Error(`Unknown option: ${argument}. ${USAGE}`)
    }
    else {
      positional.push(argument)
    }
  }
  const [image, ...names] = positional
  assert(image, USAGE)
  const unknown = names.filter(name => !PROXIES.some(proxy => proxy.name === name))
  assert.equal(unknown.length, 0, `Unknown proxy: ${unknown.join(', ')}. Known: ${PROXIES.map(proxy => proxy.name).join(', ')}`)
  return {
    image,
    stabilitySeconds,
    keepOnFailure,
    examplesDirectory,
    proxies: names.length
      ? PROXIES.filter(proxy => names.includes(proxy.name))
      : PROXIES,
  }
}

/**
 * One proxy's test environment. Every resource it creates is recorded, so that cleanup removes it on success,
 * failure, and interruption.
 */
class ProxyEnvironment {
  constructor(proxy, image, directory, certificates, examplesDirectory) {
    this.proxy = proxy
    this.proxyImage = pinnedImage(proxy.imageKey)
    this.image = image
    this.directory = directory
    this.examplesDirectory = examplesDirectory
    this.certificates = certificates
    this.prefix = `leapmux-rp-${proxy.name}-${randomUUID().slice(0, 8)}`
    this.network = this.prefix
    this.containers = []
    this.volumes = []
    this.password = `Proxy-${randomUUID()}`
  }

  log(message) {
    console.log(`[${this.proxy.name}] ${message}`)
  }

  name(role) {
    return `${this.prefix}-${role}`
  }

  // Each name is recorded BEFORE the docker call: `docker run` can create a container and then fail to start it, and
  // cleanup must still remove what the failed call left. Cleanup ignores a name that docker never created.
  async container(role, args) {
    const name = this.name(role)
    this.containers.push(name)
    await docker(['run', '-d', '--name', name, ...args])
    return name
  }

  async volume(role) {
    const name = this.name(role)
    this.volumes.push(name)
    await docker(['volume', 'create', name])
    return name
  }

  /** Run curl in the client container. Each call trusts the test authority and reaches the proxy by its host name. */
  async curl(args, options) {
    return run('docker', [
      'exec',
      '-i',
      this.client,
      'curl',
      '--silent',
      '--show-error',
      '--cacert',
      '/trust/ca.pem',
      ...args,
    ], options)
  }

  /** Call one Hub procedure through the proxy with the Connect protocol, as the web app does. */
  async rpc(method, body, cookie) {
    const result = await this.curl([
      '--dump-header',
      '-',
      '--header',
      'Content-Type: application/json',
      '--header',
      `Origin: ${HUB_URL}`,
      ...(cookie
        ? ['--header', `Cookie: ${cookie}`]
        : []),
      '--data-binary',
      '@-',
      `${HUB_URL}/leapmux.v1.${method}`,
    ], {
      input: JSON.stringify(body),
    })
    assert.equal(result.status, 0, `${method}: curl failed: ${result.stderr}`)
    const separator = result.stdout.lastIndexOf('\r\n\r\n')
    const head = result.stdout.slice(0, separator)
    const responseBody = result.stdout.slice(separator + 4)
    const status = Number(/^HTTP\/\S+ (\d+)/m.exec(head)?.[1])
    assert.equal(status, 200, `${method} answered ${status}: ${responseBody}`)
    // The Hub names the cookie __Host-leapmux-session when it sees HTTPS, through TLS of its own or through the
    // X-Forwarded-Proto header of a trusted proxy.
    const sessionCookie = /^set-cookie: ((?:__Host-)?leapmux-session=[^;]+)/im.exec(head)?.[1]
    return {
      body: JSON.parse(responseBody || '{}'),
      sessionCookie,
    }
  }

  async signIn() {
    const { body, sessionCookie } = await this.rpc('AuthService/Login', { username: ADMIN_USERNAME, password: this.password })
    assert(sessionCookie, 'Login set no session cookie')
    return { cookie: sessionCookie, userId: body.sessionReport.user.id }
  }

  /** Run the LeapMux CLI in a new container on the test network, as a remote machine does. */
  async leapmux(args, { volume } = {}) {
    return run('docker', [
      'run',
      '--rm',
      '--network',
      this.network,
      '--volume',
      `${this.trustDirectory}:/trust:ro`,
      '--volume',
      `${this.cliDirectory}:/cli`,
      ...(volume
        ? ['--volume', `${volume}:/data`]
        : []),
      '--env',
      'SSL_CERT_FILE=/trust/ca.pem',
      '--env',
      'LEAPMUX_CONTROL_CONFIG_DIR=/cli',
      '--entrypoint',
      '/usr/local/bin/leapmux',
      this.image,
      ...args,
    ], { timeoutMs: 180_000 })
  }

  async start() {
    this.trustDirectory = join(this.directory, 'trust')
    this.cliDirectory = join(this.directory, 'cli')
    mkdirSync(this.trustDirectory, { recursive: true })
    mkdirSync(this.cliDirectory, { recursive: true })
    await docker(['network', 'create', this.network])

    const hubVolume = await this.volume('hub')
    await docker([
      'run',
      '--rm',
      '--volume',
      `${hubVolume}:/data`,
      '--entrypoint',
      '/usr/local/bin/leapmux',
      this.image,
      'recover',
      'bootstrap',
      'create-admin',
      '--data-dir',
      '/data/hub',
      '--username',
      ADMIN_USERNAME,
      '--password',
      this.password,
    ])
    // The proxy joins this network namespace. Ports 80 and 443 must therefore be open to an unprivileged process
    // here, because the HAProxy image runs as one.
    this.hub = await this.container('hub', [
      '--network',
      this.network,
      '--network-alias',
      PUBLIC_HOST,
      '--sysctl',
      'net.ipv4.ip_unprivileged_port_start=0',
      '--volume',
      `${hubVolume}:/data`,
      '--env',
      'LEAPMUX_MODE=hub',
      '--env',
      'LEAPMUX_HUB_LISTEN=127.0.0.1:4327',
      this.image,
    ])

    const stockConfig = this.proxy.stockConfig
      ? await docker([
          'run',
          '--rm',
          '--entrypoint',
          'cat',
          this.proxyImage,
          this.proxy.stockConfig.path,
        ])
      : undefined
    const mounts = []
    mkdirSync(join(this.directory, 'config'), { recursive: true })
    for (const file of prepareProxyFiles(this.proxy, stockConfig, this.examplesDirectory)) {
      const local = join(this.directory, 'config', file.name)
      writeFileSync(local, file.content, { mode: 0o644 })
      mounts.push('--volume', `${local}:${file.target}:ro`)
    }
    for (const certificate of this.proxy.certificates)
      mounts.push('--volume', `${this.certificates[certificate.kind]}:${certificate.target}:ro`)
    this.proxyContainer = await this.container('proxy', [
      '--network',
      `container:${this.hub}`,
      ...mounts,
      this.proxyImage,
      ...(this.proxy.command ?? []),
    ])

    if (this.proxy.generatesCertificate) {
      const root = this.proxy.generatesCertificate.rootCertificate
      await poll('the proxy issuing its certificate authority', async () => {
        const result = await run('docker', [
          'cp',
          `${this.proxyContainer}:${root}`,
          join(this.trustDirectory, 'ca.pem'),
        ])
        return result.status === 0
          ? true
          : undefined
      })
    }
    else {
      writeFileSync(join(this.trustDirectory, 'ca.pem'), readFileSync(this.certificates.authority))
    }

    this.client = await this.container('client', [
      '--network',
      this.network,
      '--volume',
      `${this.trustDirectory}:/trust:ro`,
      '--entrypoint',
      'sleep',
      CURL_IMAGE,
      '86400',
    ])
    this.clientAddress = await docker([
      'inspect',
      '--format',
      `{{(index .NetworkSettings.Networks "${this.network}").IPAddress}}`,
      this.client,
    ])
    await poll('the Hub answering through the proxy', async () => {
      const result = await this.curl([
        '--output',
        '/dev/null',
        '--write-out',
        '%{http_code}',
        `${HUB_URL}/version`,
      ])
      if (result.status === 0 && result.stdout === '200')
        return true
      throw new Error(`curl ${result.status}, HTTP ${result.stdout}: ${result.stderr}`)
    })
    this.log('PASS: the Hub answers through the proxy')
  }

  async checkRedirect() {
    const result = await this.curl([
      '--output',
      '/dev/null',
      '--write-out',
      '%{http_code} %{redirect_url}',
      `http://${PUBLIC_HOST}/login?next=%2F`,
    ])
    assert.equal(result.status, 0, result.stderr)
    const [code, location] = result.stdout.split(' ')
    assert(['301', '308'].includes(code), `HTTP answered ${code}, not a permanent redirect`)
    assert.equal(location, `${HUB_URL}/login?next=%2F`)
    this.log(`PASS: HTTP redirects to HTTPS (${code})`)
  }

  async checkWebApp() {
    const result = await this.curl([
      '--http2',
      '--write-out',
      '\n%{http_code} %{http_version} %{content_type}',
      `${HUB_URL}/`,
    ])
    assert.equal(result.status, 0, result.stderr)
    const lines = result.stdout.trimEnd().split('\n')
    const [code, version, contentType] = lines.pop().split(' ')
    assert.equal(code, '200')
    assert.equal(version, '2', 'each example offers HTTP/2 to browsers')
    assert.match(contentType, /^text\/html/)
    assert.match(lines.join('\n'), /<div id="app">/)
    this.log('PASS: the web app loads over HTTP/2')
  }

  /**
   * Trust the proxy, then sign in again until the new session records the client's own address. The proxy's
   * X-Forwarded-For header supplies that address, and its X-Forwarded-Proto header makes the Hub issue the
   * __Host- cookie that only an HTTPS origin may hold.
   */
  async checkForwardedHeaders() {
    const { cookie } = await this.signIn()
    assert.match(cookie, /^leapmux-session=/, 'an untrusted proxy must not make the Hub see HTTPS')
    await this.rpc('UserService/ElevateSession', { currentPassword: this.password }, cookie)
    await this.rpc('AdminSettingsService/UpdateSettings', {
      writes: [
        { key: 'trusted_proxy_ranges', partialJson: TRUSTED_PROXY_RANGES },
      ],
    }, cookie)
    // The Hub applies a settings change within about 30 seconds.
    const session = await poll('a new session recording the client address', async () => {
      const candidate = await this.signIn()
      const { body } = await this.rpc('AdminUserService/ListSessions', {}, candidate.cookie)
      const newest = body.sessions.toSorted((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))[0]
      if (newest.ipAddress === this.clientAddress)
        return candidate
      throw new Error(`the newest session records ${newest.ipAddress}, not ${this.clientAddress}`)
    })
    this.log(`PASS: the Hub records the client address ${this.clientAddress}, not the proxy's`)
    assert.match(session.cookie, /^__Host-leapmux-session=/, 'the Hub must see HTTPS through X-Forwarded-Proto')
    this.log('PASS: the Hub sees HTTPS through X-Forwarded-Proto')
    return session
  }

  /**
   * Open the web app's event WebSocket and keep it open in the client container for the stability window.
   *
   * The cookie travels as a raw header. curl's cookie engine drops a __Host- cookie for a wss:// URL, although it
   * sends the same cookie for https://, so --cookie would make the handshake fail as unauthenticated.
   */
  async startWebSocket(cookie, seconds) {
    const url = `wss://${PUBLIC_HOST}${wire.ws.routes.userEvents}`
    const command = [
      'curl --silent --show-error --no-buffer --cacert /trust/ca.pem',
      `--max-time ${seconds}`,
      `--header 'Origin: ${HUB_URL}'`,
      `--header 'Sec-WebSocket-Protocol: ${wire.ws.subprotocols.userEventsRelay}'`,
      `--header 'Cookie: ${cookie}'`,
      `--output /tmp/websocket.out '${url}' 2>/tmp/websocket.err`,
      '; echo $? > /tmp/websocket.exit',
    ].join(' ')
    await docker([
      'exec',
      '--detach',
      this.client,
      'sh',
      '-c',
      command,
    ])
    await poll('the WebSocket upgrade', async () => {
      const exited = await run('docker', [
        'exec',
        this.client,
        'cat',
        '/tmp/websocket.exit',
      ])
      if (exited.status === 0) {
        throw new Error(`the WebSocket closed early with curl exit ${exited.stdout.trim()}: ${(await run('docker', [
          'exec',
          this.client,
          'cat',
          '/tmp/websocket.err',
        ])).stdout}`)
      }
      const received = await run('docker', [
        'exec',
        this.client,
        'stat',
        '-c',
        '%s',
        '/tmp/websocket.out',
      ])
      return received.status === 0 && Number(received.stdout) > 0
        ? true
        : undefined
    })
    this.log('PASS: the event WebSocket upgrades and receives its first frame')
  }

  async checkWebSocketLasted() {
    const exit = await poll('the WebSocket reaching its time limit', async () => {
      const result = await run('docker', [
        'exec',
        this.client,
        'cat',
        '/tmp/websocket.exit',
      ])
      return result.status === 0
        ? result.stdout.trim()
        : undefined
    }, (this.stabilitySeconds + 60) * 1000)
    const error = (await run('docker', [
      'exec',
      this.client,
      'cat',
      '/tmp/websocket.err',
    ])).stdout
    // curl exits with 28 when --max-time ends a transfer that is still open.
    assert.equal(exit, '28', `the WebSocket ended before its time limit with curl exit ${exit}: ${error}`)
    this.log(`PASS: the event WebSocket stayed open for ${this.stabilitySeconds} seconds`)
  }

  async registerWorker(cookie) {
    const { body } = await this.rpc('WorkerManagementService/CreateRegistrationKey', {}, cookie)
    this.workerVolume = await this.volume('worker')
    const result = await this.leapmux([
      'worker',
      'register',
      '--data-dir',
      '/data/worker',
      '--hub',
      HUB_URL,
      '--registration-key',
      body.registrationKey,
      '--timeout',
      '60s',
    ], { volume: this.workerVolume })
    assert.equal(result.status, 0, `worker register failed:\n${result.stdout}${result.stderr}`)
    this.log('PASS: the Worker registers through the proxy')
  }

  async startWorker() {
    this.worker = await this.container('worker', [
      '--network',
      this.network,
      '--volume',
      `${this.workerVolume}:/data`,
      '--volume',
      `${this.trustDirectory}:/trust:ro`,
      '--env',
      'SSL_CERT_FILE=/trust/ca.pem',
      '--env',
      'LEAPMUX_MODE=worker',
      this.image,
    ])
    this.workerId = await poll('the Worker connecting through the proxy', async () => {
      const result = await run('docker', [
        'exec',
        this.worker,
        '/usr/local/bin/leapmux',
        'worker',
        'status',
        '--data-dir',
        '/data/worker',
        '--json',
      ])
      const status = JSON.parse(result.stdout)
      if (status.readiness === 'ready' && status.connection?.state === 'connected')
        return status.registration.worker_id
      throw new Error(`readiness ${status.readiness}, connection ${status.connection?.state}`)
    })
    this.workerConnectedAt = Date.now()
    this.log(`PASS: the Worker connects through the proxy (${this.workerId})`)
  }

  /** Write a CLI credential for an API token, as `leapmux control auth login` would after its browser step. */
  async authorizeCli(cookie, userId) {
    const { body } = await this.rpc('AdminUserService/IssueAPIToken', {
      user: { id: userId },
      installationName: 'reverse-proxy-test',
      ttlSeconds: '3600',
      scopes: ['worker:read', 'file:read'],
    }, cookie)
    writeFileSync(join(this.cliDirectory, `${PUBLIC_HOST}.json`), JSON.stringify({
      hub_url: HUB_URL,
      hub_id: 'reverse-proxy-test',
      access_token: body.accessToken,
      refresh_token: '',
      expires_at: new Date(Date.now() + 3_600_000).toISOString(),
      user_id: userId,
      username: ADMIN_USERNAME,
      scope: 'worker:read file:read',
    }), { mode: 0o600 })
  }

  /**
   * Read a file of several megabytes from the Worker through an end-to-end encrypted channel. The bytes travel from
   * the Worker to the Hub on the Worker's stream, so the proxy carries them as part of one long request body.
   */
  async checkTransfer(label) {
    const path = '/tmp/reverse-proxy-transfer.txt'
    const digest = await docker([
      'exec',
      this.worker,
      'sh',
      '-c',
      `head -c ${TRANSFER_BYTES} /dev/urandom | base64 > ${path} && sha256sum ${path}`,
    ])
    const result = await this.leapmux([
      'control',
      'file',
      'read',
      '--hub',
      HUB_URL,
      '--worker-id',
      this.workerId,
      '--path',
      path,
      '--limit',
      String(2 * TRANSFER_BYTES),
    ])
    assert.equal(result.status, 0, `file read failed:\n${result.stdout}${result.stderr}`)
    const output = JSON.parse(result.stdout)
    // `leapmux control` prints a success as {"data": ...}.
    const content = output.data?.content
    assert.equal(typeof content, 'string', `file read returned no content: ${result.stdout.slice(0, 500)}`)
    assert.equal(createHash('sha256').update(content).digest('hex'), digest.split(' ')[0], 'the content changed in transit')
    this.log(`PASS: ${label}: ${(Buffer.byteLength(content) / 1024 / 1024).toFixed(1)} MiB crossed the Worker's stream intact`)
  }

  async checkWorkerStayedConnected() {
    const remaining = this.workerConnectedAt + this.stabilitySeconds * 1000 - Date.now()
    if (remaining > 0) {
      this.log(`waiting ${Math.ceil(remaining / 1000)} seconds for the stability window`)
      // The window itself is the test: a proxy timeout that ends a stream must have the time to fire.
      await sleep(remaining, undefined, { signal: interruption.signal })
    }
    const workerLogs = await run('docker', ['logs', this.worker])
    const logs = workerLogs.stdout + workerLogs.stderr
    const connects = logs.split('\n').filter(line => line.includes('connected to hub') && !line.includes('disconnected')).length
    const disconnects = logs.split('\n').filter(line => line.includes('disconnected from hub'))
    assert.equal(disconnects.length, 0, `the Worker's stream ended during the window:\n${disconnects.join('\n')}`)
    assert.equal(connects, 1, `the Worker connected ${connects} times`)
    this.log(`PASS: the Worker stayed connected for ${this.stabilitySeconds} seconds`)
  }

  async verify(stabilitySeconds) {
    this.stabilitySeconds = stabilitySeconds
    await this.start()
    await this.checkRedirect()
    await this.checkWebApp()
    const { cookie, userId } = await this.checkForwardedHeaders()
    await this.rpc('UserService/ElevateSession', { currentPassword: this.password }, cookie)
    await this.startWebSocket(cookie, stabilitySeconds)
    await this.registerWorker(cookie)
    await this.startWorker()
    await this.authorizeCli(cookie, userId)
    await this.checkTransfer('first transfer')
    await this.checkWorkerStayedConnected()
    await this.checkTransfer('transfer after the window')
    await this.checkWebSocketLasted()
  }

  async cleanup() {
    const failures = []
    // A resource that docker never created is already gone, which is the outcome cleanup wants.
    const removeEach = async (names, args, describe) => {
      for (const name of names) {
        const removed = await run('docker', [...args, name])
        if (removed.status !== 0 && !/no such|not found/i.test(removed.stderr))
          failures.push(new Error(`${describe} ${name}: ${removed.stderr}`))
      }
    }
    for (const name of this.containers) {
      const logs = await run('docker', ['logs', name])
      if (logs.status === 0)
        writeFileSync(join(this.directory, `${name}.log`), logs.stdout + logs.stderr)
    }
    await removeEach(this.containers.toReversed(), ['rm', '--force', '--volumes'], 'docker rm')
    await removeEach(this.volumes, ['volume', 'rm'], 'docker volume rm')
    await removeEach([this.network], ['network', 'rm'], 'docker network rm')
    return failures
  }
}

const { image, stabilitySeconds, keepOnFailure, examplesDirectory, proxies } = parseArguments(process.argv.slice(2))
mkdirSync(join(ROOT, '.tmp'), { recursive: true })
const directory = mkdtempSync(join(ROOT, '.tmp', 'reverse-proxy-test-'))
console.log(`Testing ${proxies.map(proxy => pinnedImage(proxy.imageKey)).join(', ')} with ${image}. Examples: ${examplesDirectory}. Diagnostics: ${directory}`)
const certificates = await createCertificates(join(directory, 'certificates'))
const environments = proxies.map((proxy) => {
  const proxyDirectory = join(directory, proxy.name)
  mkdirSync(proxyDirectory, { recursive: true })
  return new ProxyEnvironment(proxy, image, proxyDirectory, certificates, examplesDirectory)
})

process.on('SIGINT', () => {
  // The cleanup below removes every container. A second interrupt exits at once.
  if (interruption.signal.aborted)
    process.exit(130)
  console.log('Interrupted. Removing the test containers.')
  interruption.abort()
})

const results = await Promise.allSettled(environments.map(environment => environment.verify(stabilitySeconds)))
const failures = []
for (const [index, result] of results.entries()) {
  const environment = environments[index]
  if (result.status === 'rejected') {
    failures.push(result.reason)
    environment.log(`FAIL: ${result.reason.stack ?? result.reason}`)
    if (keepOnFailure && !interruption.signal.aborted) {
      // The password protects only this disposable Hub, and an inspection needs it to sign in.
      environment.log(`kept for inspection: containers ${environment.containers.join(' ')}, network ${environment.network}, sign in as ${ADMIN_USERNAME} with ${environment.password}`)
      continue
    }
  }
  failures.push(...await environment.cleanup())
}
if (failures.length > 0)
  throw new AggregateError(failures, `Reverse proxy tests failed. Diagnostics: ${directory}`)
console.log(`PASS: ${proxies.map(proxy => proxy.name).join(', ')}`)
