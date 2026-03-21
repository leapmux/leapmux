package service

import (
	"os"
	"runtime"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/version"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func registerSysInfoHandlers(d *channel.Dispatcher, svc *Context) {
	d.Register("GetWorkerSystemInfo", func(_ string, _ *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		homeDir, _ := os.UserHomeDir()
		sendProtoResponse(sender, &leapmuxv1.GetWorkerSystemInfoResponse{
			Name:    svc.Name,
			Os:      runtime.GOOS,
			Arch:    runtime.GOARCH,
			HomeDir: homeDir,
			Version: version.Value,
		})
	})
}
