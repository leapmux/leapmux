package service

import (
	"context"
	"runtime"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/util/version"
)

func registerSysInfoHandlers(d ownerOnlyRegistrar, svc *Service) {
	d.Register(leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		sendProtoResponse(sender, &leapmuxv1.GetWorkerSystemInfoResponse{
			Name: svc.Name,
			Os:   runtime.GOOS,
			Arch: runtime.GOARCH,
			// The configured home, which every path handler sanitizes against,
			// not a second read of the process environment that could disagree
			// with it. Empty when the entry point configured none.
			HomeDir: svc.HomeDir,
			// The same message the hub reports, commit time included.
			Build: version.BuildInfoProto(),
		})
	})
}
