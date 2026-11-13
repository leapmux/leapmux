package channelwire

import "github.com/leapmux/leapmux/generated/contracts"

// Worker stream values are Go-only. Browser protocols use contracts/wire.json.
const (
	WSRouteWorker               = "/ws/worker"
	WSSubprotocolWorkerStream   = "worker-stream"
	WorkerStreamMaxMessageBytes = 4 << 20
	WorkerStreamReadLimit       = WorkerStreamMaxMessageBytes + contracts.LengthPrefixBytes

	WorkerCloseReasonDeleted            = "worker_deleted"
	WorkerCloseReasonCredentialReplaced = "worker_credential_replaced"
	WorkerCloseReasonKeysDiffer         = "worker_keys_differ"
	WorkerCloseReasonInvalidMessage     = "invalid_worker_message"
	WorkerCloseReasonHelloTimeout       = "worker_hello_timeout"
	WorkerCloseReasonTooManyWorkers     = "max_workers_per_user"
	WorkerCloseReasonHubShuttingDown    = "hub_shutting_down"
	WorkerCloseReasonHubInternalError   = "hub_internal_error"
)

// WorkerStreamURL returns the worker stream's WebSocket URL.
func WorkerStreamURL(baseURL string) string { return HTTPToWS(baseURL) + WSRouteWorker }
