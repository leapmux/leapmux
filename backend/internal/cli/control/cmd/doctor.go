package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/version"
)

func newDoctorCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "doctor",
		Short:            "Check local daemons, Hub connectivity, auth, pins, and online Workers",
		QuietUnsupported: true,
		Next: []string{
			"leapmux tree",
			"leapmux worker list",
			"leapmux auth status",
		},
		Examples: []cli.Example{
			{Command: "leapmux doctor"},
			{Comment: "Inspect diagnostics as JSON.", Command: "leapmux doctor --json"},
		},
		Run: runDoctor,
	})
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok", "warn", "fail", "info"
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

type doctorWorkerCheck struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Online    bool   `json:"online"`
	PingMS    int64  `json:"ping_ms,omitempty"`
	Version   string `json:"version,omitempty"`
	Providers int    `json:"providers,omitempty"`
	Shells    int    `json:"shells,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

type doctorReport struct {
	CLIVersion string              `json:"cli_version"`
	Healthy    bool                `json:"healthy"`
	Checks     []doctorCheck       `json:"checks"`
	Workers    []doctorWorkerCheck `json:"workers,omitempty"`
}

func (r doctorReport) RenderText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "LeapMux doctor (CLI %s)\n\n", cli.SanitizeSingleLine(r.CLIVersion)); err != nil {
		return err
	}
	for _, chk := range r.Checks {
		tag := "[" + chk.Status + "]"
		if _, err := fmt.Fprintf(w, "  %-6s %-18s %s\n", tag, cli.SanitizeSingleLine(chk.Name), cli.SanitizeSingleLine(chk.Detail)); err != nil {
			return err
		}
		if chk.Fix != "" {
			if _, err := fmt.Fprintf(w, "         fix: %s\n", cli.SanitizeSingleLine(chk.Fix)); err != nil {
				return err
			}
		}
	}
	if len(r.Workers) > 0 {
		if _, err := fmt.Fprintln(w, "\nWorkers:"); err != nil {
			return err
		}
		for _, wk := range r.Workers {
			label := wk.Name
			if label == "" {
				label = cli.ShortID(wk.ID)
			}
			if !wk.Online {
				if _, err := fmt.Fprintf(w, "  [warn] %-18s offline\n", cli.SanitizeSingleLine(label)); err != nil {
					return err
				}
				continue
			}
			if wk.Error != "" {
				if _, err := fmt.Fprintf(w, "  [fail] %-18s %s\n", cli.SanitizeSingleLine(label), cli.SanitizeSingleLine(wk.Error)); err != nil {
					return err
				}
				continue
			}
			tag := "[" + wk.Status + "]"
			if _, err := fmt.Fprintf(w, "  %-6s %-18s ping=%dms  providers=%d  shells=%d  version=%s\n",
				tag, cli.SanitizeSingleLine(label), wk.PingMS, wk.Providers, wk.Shells, cli.SanitizeSingleLine(wk.Version)); err != nil {
				return err
			}
			if wk.Status == "warn" && wk.Version != "" && wk.Version != r.CLIVersion {
				if _, err := fmt.Fprintf(w, "         fix: upgrade worker or CLI so versions match (CLI is %s)\n", cli.SanitizeSingleLine(r.CLIVersion)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func runDoctor(inv *cli.Invocation) (any, error) {
	report := doctorReport{
		CLIVersion: version.Format(),
		Healthy:    true,
	}
	addCheck := func(chk doctorCheck) {
		if chk.Status == "fail" {
			report.Healthy = false
		}
		report.Checks = append(report.Checks, chk)
	}

	// 1. Local daemons (solo, hub, worker, dev).
	addCheck(checkLocalDaemons(inv.Context))

	// 2. Client / Hub / Auth resolution.
	c, err := newClient(inv)
	if err != nil {
		addCheck(doctorCheck{
			Name:   "connection",
			Status: "fail",
			Detail: err.Error(),
			Fix:    `run "leapmux up" to start a local instance, or "leapmux auth login --hub <url>" to sign in`,
		})
		if err := inv.Printer.Result(report); err != nil {
			return nil, err
		}
		return nil, &cli.ExitStatus{Code: cli.ExitFailure}
	}

	ctx, cancel := context.WithTimeout(inv.Context, 5*time.Second)
	defer cancel()

	if c.IsWorkerIPC() {
		addCheck(doctorCheck{
			Name:   "connection",
			Status: "ok",
			Detail: "connected through local tab IPC socket",
		})
	} else {
		addCheck(doctorCheck{
			Name:   "connection",
			Status: "ok",
			Detail: "hub " + c.HubURL,
		})
		user, warning, outcome, checkErr := hubCheck(c)
		switch outcome {
		case hubCheckConfirmed:
			detail := fmt.Sprintf("authenticated as %s (id %s)", user.GetUsername(), cli.ShortID(user.GetId()))
			status := "ok"
			fix := ""
			if creds, cerr := control.LoadCredentials(c.HubURL); cerr == nil && creds != nil {
				if creds.Scope != "" {
					detail += fmt.Sprintf(", scopes=%s", creds.Scope)
				}
				if !creds.ExpiresAt.IsZero() {
					rem := time.Until(creds.ExpiresAt).Round(time.Second)
					if rem <= 0 {
						status = "warn"
						detail += " (access token expired; will refresh)"
					} else {
						detail += fmt.Sprintf(", token expires in %s", rem)
					}
				}
				if !creds.RefreshExpiresAt.IsZero() && time.Until(creds.RefreshExpiresAt) <= 24*time.Hour {
					status = "warn"
					fix = fmt.Sprintf(`refresh soon with "leapmux auth login --hub %s"`, c.HubURL)
				}
			}
			addCheck(doctorCheck{
				Name:   "auth",
				Status: status,
				Detail: detail,
				Fix:    fix,
			})
			if user.GetIsAdmin() {
				var netResp leapmuxv1.GetListenStatusResponse
				if netErr := hubCallUnary(ctx, c, leapmuxv1connect.AdminNetworkServiceGetListenStatusProcedure,
					&leapmuxv1.GetListenStatusRequest{}, &netResp); netErr == nil {
					var addrs []string
					for _, b := range netResp.GetBound() {
						if b.GetError() == "" && b.GetAddress() != "" {
							addrs = append(addrs, b.GetAddress())
						}
					}
					addrs = append(addrs, netResp.GetLocalIpcAddresses()...)
					addCheck(doctorCheck{
						Name:   "admin-network",
						Status: "ok",
						Detail: fmt.Sprintf("%d active listener(s): %s", len(addrs), strings.Join(addrs, ", ")),
					})
				}
			}
		case hubCheckLocalFallback:
			addCheck(doctorCheck{
				Name:   "auth",
				Status: "warn",
				Detail: warning,
				Fix:    fmt.Sprintf(`check that %s is reachable, or run "leapmux auth status --hub %s"`, c.HubURL, c.HubURL),
			})
		default:
			addCheck(doctorCheck{
				Name:   "auth",
				Status: "fail",
				Detail: checkErr.Error(),
				Fix:    fmt.Sprintf(`run "leapmux auth login --hub %s"`, c.HubURL),
			})
		}
	}

	// 3. Hub version skew check.
	var sysResp leapmuxv1.GetSystemInfoResponse
	if err := hubCallUnary(ctx, c, leapmuxv1connect.SystemServiceGetSystemInfoProcedure, &leapmuxv1.GetSystemInfoRequest{}, &sysResp); err != nil {
		addCheck(doctorCheck{
			Name:   "hub",
			Status: "fail",
			Detail: "GetSystemInfo failed: " + err.Error(),
			Fix:    "verify the Hub is running and reachable",
		})
	} else {
		hubBuild := version.FormatBuild(sysResp.GetBuild())
		if hubBuild != "" && hubBuild != version.Format() {
			addCheck(doctorCheck{
				Name:   "hub",
				Status: "warn",
				Detail: fmt.Sprintf("reachable (build %s; CLI is %s)", hubBuild, version.Format()),
				Fix:    "upgrade the older binary so CLI and Hub versions match",
			})
		} else {
			detail := "reachable"
			if hubBuild != "" {
				detail = "reachable (" + hubBuild + ")"
			}
			addCheck(doctorCheck{
				Name:   "hub",
				Status: "ok",
				Detail: detail,
			})
		}
	}

	// 4. TOFU pin store check (when not inside Worker IPC).
	if !c.IsWorkerIPC() {
		pins, pinErr := openPinStore(inv.Command)
		if pinErr != nil {
			addCheck(doctorCheck{
				Name:   "worker-pins",
				Status: "warn",
				Detail: pinErr.Error(),
			})
		} else if entries, listErr := pins.List(ctx); listErr != nil {
			addCheck(doctorCheck{
				Name:   "worker-pins",
				Status: "fail",
				Detail: "pin store unreadable: " + listErr.Error(),
				Fix:    `inspect pins with "leapmux worker pins list"`,
			})
		} else {
			addCheck(doctorCheck{
				Name:   "worker-pins",
				Status: "ok",
				Detail: fmt.Sprintf("%d pinned %s", len(entries), pluralizeWord(len(entries), "worker", "workers")),
			})
		}
	}

	// 5. Workers: Ping RTT, version skew, providers & shells counts.
	workers, wErr := listAllWorkers(ctx, c)
	if wErr != nil {
		addCheck(doctorCheck{
			Name:   "workers",
			Status: "warn",
			Detail: "could not list workers: " + wErr.Error(),
		})
	} else {
		report.Workers = probeDoctorWorkers(ctx, c, workers)
		onlineCount := 0
		failedCount := 0
		for _, wk := range report.Workers {
			if wk.Online {
				onlineCount++
				if wk.Status == "fail" {
					failedCount++
				}
			}
		}
		if failedCount > 0 {
			addCheck(doctorCheck{
				Name:   "workers",
				Status: "fail",
				Detail: fmt.Sprintf("%d/%d online, %d failed E2EE probe", onlineCount, len(workers), failedCount),
				Fix:    `check Worker logs with "leapmux daemon worker logs" or "leapmux worker pins list"`,
			})
		} else {
			addCheck(doctorCheck{
				Name:   "workers",
				Status: "ok",
				Detail: fmt.Sprintf("%d/%d online", onlineCount, len(workers)),
			})
		}
	}

	if !report.Healthy {
		if err := inv.Printer.Result(report); err != nil {
			return nil, err
		}
		return nil, &cli.ExitStatus{Code: cli.ExitFailure}
	}
	return report, nil
}

func checkLocalDaemons(ctx context.Context) doctorCheck {
	var parent string
	if strings.TrimSpace(os.Getenv("LEAPMUX_CONTROL_CONFIG_DIR")) != "" {
		if cfgDir, err := control.ConfigDir(); err == nil {
			parent = filepath.Dir(cfgDir)
		}
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		parent = filepath.Join(home, ".config", "leapmux")
	} else if cfgDir, err := control.ConfigDir(); err == nil {
		parent = filepath.Dir(cfgDir)
	}
	if parent == "" {
		return doctorCheck{Name: "local-daemons", Status: "info", Detail: "config directory unavailable"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var running []string
	for _, mode := range []string{"solo", "hub", "worker", "dev"} {
		st, err := lifecycle.Observe(probeCtx, filepath.Join(parent, mode))
		if err == nil && st.Process.State == lifecycle.ProcessRunning {
			running = append(running, mode)
		}
	}
	if len(running) == 0 {
		return doctorCheck{
			Name:   "local-daemons",
			Status: "info",
			Detail: "no local daemon running in default directories",
		}
	}
	return doctorCheck{
		Name:   "local-daemons",
		Status: "ok",
		Detail: "running: " + strings.Join(running, ", "),
	}
}

func probeDoctorWorkers(ctx context.Context, c *control.Client, workers []*leapmuxv1.Worker) []doctorWorkerCheck {
	results := make([]doctorWorkerCheck, len(workers))
	var g errgroup.Group
	g.SetLimit(8)
	var mu sync.Mutex
	for i, w := range workers {
		wk := w
		idx := i
		if !wk.GetOnline() {
			results[idx] = doctorWorkerCheck{ID: wk.GetId(), Online: false, Status: "warn"}
			continue
		}
		g.Go(func() error {
			check := doctorWorkerCheck{ID: wk.GetId(), Online: true, Status: "ok"}
			probeErr := withWorkerChannel(ctx, c, wk.GetId(), func(call workerCall) error {
				start := time.Now()
				var pingResp leapmuxv1.PingResponse
				if err := call.Call(ctx, leapmuxv1connect.WorkerSystemServicePingProcedure, &leapmuxv1.PingRequest{}, &pingResp); err != nil {
					return fmt.Errorf("ping failed: %w", err)
				}
				check.PingMS = time.Since(start).Milliseconds()
				var infoResp leapmuxv1.GetWorkerSystemInfoResponse
				if err := call.Call(ctx, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &infoResp); err != nil {
					return fmt.Errorf("system info failed: %w", err)
				}
				check.Name = infoResp.GetName()
				check.Version = version.FormatBuild(infoResp.GetBuild())
				if check.Version != "" && check.Version != version.Format() {
					check.Status = "warn"
				}
				var provResp leapmuxv1.ListAvailableProvidersResponse
				if err := call.Call(ctx, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, &leapmuxv1.ListAvailableProvidersRequest{}, &provResp); err == nil {
					check.Providers = len(provResp.GetProviders())
				}
				var shellResp leapmuxv1.ListAvailableShellsResponse
				if err := call.Call(ctx, leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, &leapmuxv1.ListAvailableShellsRequest{}, &shellResp); err == nil {
					check.Shells = len(shellResp.GetShells())
				}
				return nil
			})
			if probeErr != nil {
				check.Status = "fail"
				check.Error = probeErr.Error()
			}
			mu.Lock()
			results[idx] = check
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return results
}
