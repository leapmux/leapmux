import type { McpProbeServer } from './mcpProbeServer'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { findBinary } from './binaryOnPath'
import { writeMcpFormServer } from './mcpFormServer'
import { writePrivateJSON } from './privateConfigFile'
import { qoderEndpointCacheRecords } from './qoderSurface'
import { quotePosixShellArgument } from './shellArguments'

export interface QoderEnvironmentOptions {
  /** The isolated HOME of the run. */
  homeDir: string
  /** The directory of private command wrappers, first on the run's PATH. */
  shimsDirectory: string
  /** The origin of the mock, which serves Qoder's account and endpoint services. */
  origin: string
  /** The OpenAI-compatible base URL of the mock, which ends in `/v1`. */
  baseURL: string
  modelKey: string
  /** The default model of the custom provider. */
  modelID: string
  /** A second model, so a spec can switch models. */
  alternateModelID: string
}

/**
 * Configure isolated Qoder authentication and endpoint discovery.
 *
 * The native headless mode requires an authenticated account before stream-json starts.
 * QODER_SDK_AUTH_PAYLOAD_FILE supplies a fake access token through initFromAccessToken.
 * QODER_AGENT_SDK_ENTRYPOINT selects this software development kit (SDK) path.
 * The CLI reads the file once, so the private launcher writes it before each start.
 *
 * qoderEndpointCaches supplies each elected endpoint through both native cache formats.
 * Authentication calls reach handleQoderHttp in ./qoderSurface. Model calls reach ./mockModelServer.
 * A modelConfigs.customModels entry beside providers causes a native catalog conflict.
 * The fixture therefore supplies one provider registration.
 *
 * The Worker passes --config-dir for private configuration.
 * These settings select GLOBAL, omit user rc files, use file credentials, and disable Alibaba HTTPDNS.
 */
export function createQoderEnvironment(options: QoderEnvironmentOptions): Record<string, string> {
  const qoderHome = join(options.homeDir, '.qoder')
  mkdirSync(qoderHome, { recursive: true })
  const formServer = writeMcpFormServer(qoderHome, 'form-server.mjs')
  writePrivateJSON(join(qoderHome, 'settings.json'), qoderSettings(options, formServer))
  qoderEndpointCaches(qoderHome, options.origin)
  // The CLI reads this SDK payload once at startup.
  // It contains the known mock key, and the private endpoints never reach a real account.
  const authPayloadPath = join(qoderHome, 'qoder-sdk-auth.json')
  const authPayload = { type: 'accessToken', accessToken: options.modelKey }
  writePrivateJSON(authPayloadPath, authPayload)
  const installedQoder = findBinary('qodercli')
  if (installedQoder !== null && process.platform !== 'win32') {
    writeFileSync(join(options.shimsDirectory, 'qodercli'), `#!/bin/sh
set -eu
if [ -z "$QODER_SDK_AUTH_PAYLOAD_FILE" ]; then
  exit 1
fi
printf '%s\\n' ${quotePosixShellArgument(JSON.stringify(authPayload))} > "$QODER_SDK_AUTH_PAYLOAD_FILE"
exec ${quotePosixShellArgument(installedQoder)} "$@"
`, { mode: 0o755 })
  }
  return {
    QODER_SITE: 'GLOBAL',
    // Select prod without a region suffix. The native format is "<env>-<region>".
    // A region other than auto elects only securityInference.
    // The openapi token exchange would then reach the real openapi.qoder.sh service.
    QODER_ENV: 'prod',
    QODER_NO_RC: '1',
    QODER_FORCE_FILE_STORAGE: '1',
    QODER_HTTPDNS: '0',
    // The mocked-auth recipe. See the block comment above.
    QODER_AGENT_SDK_ENTRYPOINT: '1',
    QODER_SDK_AUTH_PAYLOAD_FILE: authPayloadPath,
    // The SDK requires this switch to register the custom provider.
    // Without it, isCustomProviderEntryEnabled() returns false and the model call reaches the real Qoder API.
    QODER_SDK_CUSTOM_BASE_URL_BYOK: '1',
    // Qoder treats an empty token as unset. This prevents the user's token from reaching the test.
    // The mock serves authentication with its own fixed credential.
    QODER_PERSONAL_ACCESS_TOKEN: '',
    QODER_SESSION_ID: '',
    QODER_CLI: '',
    QODERCN_CLI: '',
    QODER_REMOTE_CHILD: '',
    // Qoder's own updater runs only in its interactive UI. A different download
    // runs in `-p` mode: the Qoder Security plugin loads unless each of its four
    // checks is explicitly false, and then downloads `qodersec` and a pinned
    // `qodercli` under $HOME. SDK mode, which this fixture selects, defaults the
    // four checks to off; this value does not depend on that default. Each key must
    // be a literal false, and the value must be valid JSON, or Qoder ignores it.
    QODER_SECURITY_SCAN_SETTINGS_JSON: JSON.stringify({ l1StaticCheck: false, l2LightweightScan: false, l3DeepScan: false, gitPushScanHook: false }),
    QODERSEC_SKIP_ASYNC_UPDATE: '1',
    QODER_CONFIG_SERVICE_URL: options.origin,
    QODER_SERVER_ENDPOINT: '',
  }
}

/**
 * Configure Qoder's private custom provider.
 *
 * Qoder reads settings.json from --config-dir and selects each model as <provider>/<model>.
 * One providers entry registers the model.
 * A modelConfigs.customModels entry for the same key registers it twice.
 * Qoder then rejects the provider with "model key ... conflicts with an existing catalog model".
 * The next model call would reach the real Qoder API, so this fixture uses providers alone.
 */
function qoderSettings(options: QoderEnvironmentOptions, formServer: McpProbeServer): Record<string, unknown> {
  return {
    mcpServers: {
      [formServer.name]: { command: formServer.command, args: [...formServer.args] },
    },
    providers: {
      mockprov: {
        type: 'openai-compatible',
        protocol: 'openai',
        authType: 'bearer',
        // The schema spells the key `baseUrl` (camelCase, not `baseURL`).
        baseUrl: options.baseURL,
        apiKey: options.modelKey,
        displayName: 'Mock Provider',
        models: [
          { model: options.modelID, displayName: 'Mock Model', capabilities: { vision: true } },
          { model: options.alternateModelID, displayName: 'Alternate Mock Model', capabilities: { vision: true } },
        ],
      },
    },
  }
}

/**
 * Set every elected Qoder endpoint to the mock through its native V1 and V2 caches.
 *
 * A cold native cache selects real *.qoder.sh endpoints for authentication and model calls.
 * The auth path requires V1. Its absence causes access_token_invalid before token exchange.
 * Discovery refresh writes V2. Both formats expire after 24 hours.
 * The fixture writes a fresh updatedAt for each run.
 */
function qoderEndpointCaches(qoderHome: string, origin: string): void {
  const cacheDir = join(qoderHome, '.cache')
  mkdirSync(cacheDir, { recursive: true })
  const { v1, v2 } = qoderEndpointCacheRecords(origin, Date.now())
  writePrivateJSON(join(cacheDir, 'qoder-client-endpoint-cache.json'), v2)
  writePrivateJSON(join(cacheDir, 'qoder-client-endpoint-cache-public.json'), v2)
  writePrivateJSON(join(cacheDir, 'endpoint-cache.json'), v1)
}
