package service

import (
	"context"
	"os"
	"runtime"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/util/version"
)

func registerSysInfoHandlers(d ownerOnlyRegistrar, svc *Service) {
	d.Register(leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		homeDir, _ := os.UserHomeDir()
		sendProtoResponse(sender, &leapmuxv1.GetWorkerSystemInfoResponse{
			Name:    svc.Name,
			Os:      runtime.GOOS,
			Arch:    runtime.GOARCH,
			HomeDir: homeDir,
			// The same message the hub reports, commit time included.
			Build: version.BuildInfoProto(),
		})
	})
}
