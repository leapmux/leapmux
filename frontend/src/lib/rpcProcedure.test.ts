import { describe, expect, it } from 'vitest'
import { WorkerAgentService } from '~/generated/proto/leapmux/v1/agent_pb'
import { WorkerTunnelService } from '~/generated/proto/leapmux/v1/tunnel_pb'
import { WorkerWatchService } from '~/generated/proto/leapmux/v1/watch_pb'
import { WorkerSystemService } from '~/generated/proto/leapmux/v1/worker_system_pb'
import { procedureOf } from './rpcProcedure'

describe('procedureOf', () => {
  it('returns the connect procedure path of a unary method', () => {
    expect(procedureOf(WorkerAgentService.method.openAgent)).toBe('/leapmux.v1.WorkerAgentService/OpenAgent')
  })

  it('returns the connect procedure path of a server-streaming method', () => {
    expect(procedureOf(WorkerWatchService.method.watchEvents)).toBe('/leapmux.v1.WorkerWatchService/WatchEvents')
    expect(procedureOf(WorkerTunnelService.method.openTunnelConn)).toBe('/leapmux.v1.WorkerTunnelService/OpenTunnelConn')
  })

  it('keeps the declared method name rather than the camel-case local name', () => {
    expect(procedureOf(WorkerSystemService.method.ping)).toBe('/leapmux.v1.WorkerSystemService/Ping')
    expect(procedureOf(WorkerSystemService.method.getWorkerSystemInfo)).toBe('/leapmux.v1.WorkerSystemService/GetWorkerSystemInfo')
  })
})
