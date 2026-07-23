package service

import (
	"context"
	"os"
	"runtime"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/util/version"
)

func registerSysInfoHandlers(d ownerOnlyRegistrar, svc *Service) {
	d.Register("GetWorkerSystemInfo", func(_ context.Context, _ userid.UserID, _ *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		homeDir, _ := os.UserHomeDir()
		sendProtoResponse(sender, &leapmuxv1.GetWorkerSystemInfoResponse{
			Name:       svc.Name,
			Os:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			HomeDir:    homeDir,
			Version:    version.Value,
			CommitHash: version.CommitHash,
			BuildTime:  version.BuildTime,
			Branch:     version.Branch,
		})
	})
}
