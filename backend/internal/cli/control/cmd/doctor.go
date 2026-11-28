package cmd

import (
	"context"
	"errors"
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
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/version"
)

// doctorCheckTimeout caps each independent doctor RPC check so one slow check
// cannot exhaust the deadline of another.
const doctorCheckTimeout = 5 * time.Second

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
	PingMS    *int64 `json:"ping_ms,omitempty"`
	Version   string `json:"version,omitempty"`
	Providers *int   `json:"providers,omitempty"`
	Shells    *int   `json:"shells,omitempty"`
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
			var parts []string
			if wk.PingMS != nil {
				parts = append(parts, fmt.Sprintf("ping=%dms", *wk.PingMS))
			}
			if wk.Providers != nil {
				parts = append(parts, fmt.Sprintf("providers=%d", *wk.Providers))
			}
			if wk.Shells != nil {
				parts = append(parts, fmt.Sprintf("shells=%d", *wk.Shells))
			}
			if wk.Version != "" {
				parts = append(parts, "version="+cli.SanitizeSingleLine(wk.Version))
			}
			if _, err := fmt.Fprintf(w, "  %-6s %-18s %s\n", tag, cli.SanitizeSingleLine(label), strings.Join(parts, "  ")); err != nil {
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
		return finishDoctorReport(inv, report)
	}

	var (
		hc      *control.HubClient
		isAdmin bool
	)
	switch peer := c.(type) {
	case *control.WorkerIPCClient:
		connDetail := "connected through local tab IPC socket"
		if envHub := strings.TrimSpace(os.Getenv("LEAPMUX_HUB")); envHub != "" {
			connDetail += fmt.Sprintf(" ($LEAPMUX_HUB=%s ignored; $LEAPMUX_CONTROL_SOCK takes precedence)", envHub)
		}
		addCheck(doctorCheck{
			Name:   "connection",
			Status: "ok",
			Detail: connDetail,
		})
		addCheck(checkDoctorTabDirectHubAuth())
	case *control.HubClient:
		hc = peer
		addCheck(doctorCheck{
			Name:   "connection",
			Status: "ok",
			Detail: "hub " + hc.HubURL,
		})
		authCheck, confirmedAdmin := checkDoctorAuth(inv.Context, hc)
		isAdmin = confirmedAdmin
		addCheck(authCheck)
	}

	// 3. Independent post-auth checks run concurrently with separate deadlines
	// derived from inv.Context, then append in deterministic order.
	var (
		adminNetCheck *doctorCheck
		hubCheck      doctorCheck
		pinsCheck     *doctorCheck
		workersCheck  doctorCheck
		workerReports []doctorWorkerCheck
		wg            sync.WaitGroup
	)
	if hc != nil && isAdmin {
		wg.Add(1)
		go func() {
			defer wg.Done()
			adminNetCheck = checkDoctorAdminNetwork(inv.Context, hc)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		hubCheck = checkDoctorHubVersion(inv.Context, c)
	}()
	if hc != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chk := checkDoctorPins(inv.Context, hc.HubURL)
			pinsCheck = &chk
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		workersCheck, workerReports = checkDoctorWorkers(inv.Context, c)
	}()
	wg.Wait()

	if adminNetCheck != nil {
		addCheck(*adminNetCheck)
	}
	addCheck(hubCheck)
	if pinsCheck != nil {
		addCheck(*pinsCheck)
	}
	addCheck(workersCheck)
	report.Workers = workerReports

	return finishDoctorReport(inv, report)
}

func finishDoctorReport(inv *cli.Invocation, report doctorReport) (any, error) {
	if !report.Healthy {
		if err := inv.Printer.Result(report); err != nil {
			return nil, err
		}
		return nil, &cli.ExitStatus{Code: cli.ExitFailure}
	}
	return report, nil
}

func checkDoctorTabDirectHubAuth() doctorCheck {
	hub, source, err := control.DefaultHubWithSource()
	if err != nil {
		var noHub *control.NoHubError
		if errors.As(err, &noHub) {
			if noHub.Stored > 1 {
				return doctorCheck{
					Name:   "direct_hub_auth",
					Status: "warn",
					Detail: fmt.Sprintf("no default hub selected for direct CLI use (%d stored credentials)", noHub.Stored),
					Fix:    `run "leapmux auth use <hub>" to select a default hub`,
				}
			}
			return doctorCheck{
				Name:   "direct_hub_auth",
				Status: "info",
				Detail: "no direct hub configured for outside-tab CLI use (tab IPC handles in-tab commands)",
			}
		}
		return doctorCheck{
			Name:   "direct_hub_auth",
			Status: "warn",
			Detail: fmt.Sprintf("could not read direct hub config: %v", err),
		}
	}

	sourceLabel := doctorHubSourceLabel(source)
	precedenceNote := ""
	if source == control.HubSourceEnv {
		precedenceNote = "; $LEAPMUX_CONTROL_SOCK takes precedence inside this tab"
	}

	creds, loadErr := control.LoadCredentials(hub)
	switch {
	case loadErr == nil && creds != nil:
		userLabel := creds.Username
		if userLabel == "" {
			userLabel = cli.ShortID(creds.UserID)
		}
		if !creds.RefreshExpiresAt.IsZero() && !time.Now().Before(creds.RefreshExpiresAt) {
			return doctorCheck{
				Name:   "direct_hub_auth",
				Status: "warn",
				Detail: fmt.Sprintf("direct hub %s (%s): stored credential for %s refresh token expired%s", hub, sourceLabel, userLabel, precedenceNote),
				Fix:    fmt.Sprintf(`run "%s"`, authLoginCommand(hub)),
			}
		}
		if !creds.ExpiresAt.IsZero() && !time.Now().Before(creds.ExpiresAt) {
			return doctorCheck{
				Name:   "direct_hub_auth",
				Status: "ok",
				Detail: fmt.Sprintf("direct hub %s (%s): stored credential for %s (access token expired; will refresh)%s", hub, sourceLabel, userLabel, precedenceNote),
			}
		}
		return doctorCheck{
			Name:   "direct_hub_auth",
			Status: "ok",
			Detail: fmt.Sprintf("direct hub %s (%s): stored credential for %s%s", hub, sourceLabel, userLabel, precedenceNote),
		}
	case errors.Is(loadErr, control.ErrNotLoggedIn):
		if locallisten.IsLocal(hub) {
			return doctorCheck{
				Name:   "direct_hub_auth",
				Status: "ok",
				Detail: fmt.Sprintf("direct hub %s (%s): local IPC hub (no stored credential required)%s", hub, sourceLabel, precedenceNote),
			}
		}
		return doctorCheck{
			Name:   "direct_hub_auth",
			Status: "warn",
			Detail: fmt.Sprintf("direct hub %s (%s): no stored credential%s", hub, sourceLabel, precedenceNote),
			Fix:    fmt.Sprintf(`run "%s"`, authLoginCommand(hub)),
		}
	default:
		return doctorCheck{
			Name:   "direct_hub_auth",
			Status: "warn",
			Detail: fmt.Sprintf("direct hub %s (%s): stored credential unreadable (%v)%s", hub, sourceLabel, loadErr, precedenceNote),
			Fix:    fmt.Sprintf(`run "%s" to replace it`, authLoginCommand(hub)),
		}
	}
}

func doctorHubSourceLabel(source control.HubSource) string {
	switch source {
	case control.HubSourceEnv:
		return "$LEAPMUX_HUB"
	case control.HubSourceContext:
		return "context.json"
	case control.HubSourceSingleCredential:
		return "stored credential"
	case control.HubSourceDiscovered:
		return "local hub discovery"
	default:
		return "configured hub"
	}
}

func checkDoctorAuth(ctx context.Context, hc *control.HubClient) (doctorCheck, bool) {
	check, err := checkHubSession(ctx, hc)
	if err != nil {
		return doctorCheck{
			Name:   "auth",
			Status: "fail",
			Detail: err.Error(),
			Fix:    fmt.Sprintf(`run "%s"`, authLoginCommand(hc.HubURL)),
		}, false
	}
	if check.Fallback {
		return doctorCheck{
			Name:   "auth",
			Status: "warn",
			Detail: check.Warning,
			Fix:    fmt.Sprintf(`check that %s is reachable, or run "leapmux auth status --hub %s"`, hc.HubURL, hc.HubURL),
		}, false
	}
	user := check.Report.GetUser()
	detail := fmt.Sprintf("authenticated as %s (id %s)", user.GetUsername(), cli.ShortID(user.GetId()))
	status := "ok"
	fix := ""
	if creds, cerr := control.LoadCredentials(hc.HubURL); cerr == nil && creds != nil {
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
			fix = fmt.Sprintf(`refresh soon with "%s"`, authLoginCommand(hc.HubURL))
		}
	}
	return doctorCheck{
		Name:   "auth",
		Status: status,
		Detail: detail,
		Fix:    fix,
	}, user.GetIsAdmin()
}

func checkDoctorAdminNetwork(ctx context.Context, hc *control.HubClient) *doctorCheck {
	netCtx, cancel := context.WithTimeout(ctx, doctorCheckTimeout)
	defer cancel()
	netResp, err := hc.AdminNetworkService().GetListenStatus(netCtx, &leapmuxv1.GetListenStatusRequest{})
	if err != nil {
		return nil
	}
	var (
		addrs  []string
		failed []string
	)
	for _, b := range netResp.GetBound() {
		if b.GetError() != "" {
			if b.GetAddress() != "" {
				failed = append(failed, fmt.Sprintf("%s (%s)", b.GetAddress(), b.GetError()))
			} else {
				failed = append(failed, b.GetError())
			}
			continue
		}
		if b.GetAddress() != "" {
			addrs = append(addrs, b.GetAddress())
		}
	}
	addrs = append(addrs, netResp.GetLocalIpcAddresses()...)
	if len(failed) > 0 {
		status := "warn"
		detail := fmt.Sprintf("%d active listener(s): %s; %d failed: %s",
			len(addrs), strings.Join(addrs, ", "), len(failed), strings.Join(failed, ", "))
		if len(addrs) == 0 {
			status = "fail"
			detail = fmt.Sprintf("0 active listeners; %d failed: %s", len(failed), strings.Join(failed, ", "))
		}
		return &doctorCheck{
			Name:   "admin-network",
			Status: status,
			Detail: detail,
			Fix:    `inspect listener errors with "leapmux admin network status"`,
		}
	}
	return &doctorCheck{
		Name:   "admin-network",
		Status: "ok",
		Detail: fmt.Sprintf("%d active listener(s): %s", len(addrs), strings.Join(addrs, ", ")),
	}
}

func checkDoctorHubVersion(ctx context.Context, c control.Peer) doctorCheck {
	hubCtx, cancel := context.WithTimeout(ctx, doctorCheckTimeout)
	defer cancel()
	var sysResp leapmuxv1.GetSystemInfoResponse
	if err := hubCallUnary(hubCtx, c, leapmuxv1connect.SystemServiceGetSystemInfoProcedure, &leapmuxv1.GetSystemInfoRequest{}, &sysResp); err != nil {
		return doctorCheck{
			Name:   "hub",
			Status: "fail",
			Detail: "GetSystemInfo failed: " + err.Error(),
			Fix:    "verify the Hub is running and reachable",
		}
	}
	hubBuild := version.FormatBuild(sysResp.GetBuild())
	if hubBuild != "" && hubBuild != version.Format() {
		return doctorCheck{
			Name:   "hub",
			Status: "warn",
			Detail: fmt.Sprintf("reachable (build %s; CLI is %s)", hubBuild, version.Format()),
			Fix:    "upgrade the older binary so CLI and Hub versions match",
		}
	}
	detail := "reachable"
	if hubBuild != "" {
		detail = "reachable (" + hubBuild + ")"
	}
	return doctorCheck{
		Name:   "hub",
		Status: "ok",
		Detail: detail,
	}
}

func checkDoctorPins(ctx context.Context, hubURL string) doctorCheck {
	pinCtx, cancel := context.WithTimeout(ctx, doctorCheckTimeout)
	defer cancel()
	pins, pinErr := control.NewPinStore(hubURL)
	if pinErr != nil {
		return doctorCheck{
			Name:   "worker-pins",
			Status: "warn",
			Detail: pinErr.Error(),
		}
	}
	entries, listErr := pins.List(pinCtx)
	if listErr != nil {
		return doctorCheck{
			Name:   "worker-pins",
			Status: "fail",
			Detail: "pin store unreadable: " + listErr.Error(),
			Fix:    `inspect pins with "leapmux worker pins list"`,
		}
	}
	return doctorCheck{
		Name:   "worker-pins",
		Status: "ok",
		Detail: pluralize(len(entries), "pinned worker"),
	}
}

func checkDoctorWorkers(ctx context.Context, c control.Peer) (doctorCheck, []doctorWorkerCheck) {
	workerCtx, cancel := context.WithTimeout(ctx, doctorCheckTimeout)
	defer cancel()
	workers, err := listAllWorkers(workerCtx, c)
	if err != nil {
		return doctorCheck{
			Name:   "workers",
			Status: "warn",
			Detail: "could not list workers: " + err.Error(),
		}, nil
	}
	results := probeDoctorWorkers(workerCtx, c, workers)
	onlineCount := 0
	failedCount := 0
	for _, wk := range results {
		if wk.Online {
			onlineCount++
			if wk.Status == "fail" {
				failedCount++
			}
		}
	}
	if failedCount > 0 {
		return doctorCheck{
			Name:   "workers",
			Status: "fail",
			Detail: fmt.Sprintf("%d/%d online, %d failed E2EE probe", onlineCount, len(workers), failedCount),
			Fix:    `check Worker logs with "leapmux daemon worker logs" or "leapmux worker pins list"`,
		}, results
	}
	return doctorCheck{
		Name:   "workers",
		Status: "ok",
		Detail: fmt.Sprintf("%d/%d online", onlineCount, len(workers)),
	}, results
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
	modes := []string{"solo", "hub", "worker", "dev"}
	up := make([]bool, len(modes))
	var wg sync.WaitGroup
	wg.Add(len(modes))
	for i, mode := range modes {
		go func(idx int, m string) {
			defer wg.Done()
			st, err := lifecycle.Observe(probeCtx, filepath.Join(parent, m))
			if err == nil && st.Process.State == lifecycle.ProcessRunning {
				up[idx] = true
			}
		}(i, mode)
	}
	wg.Wait()
	var running []string
	for i, mode := range modes {
		if up[i] {
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

func probeDoctorWorkers(ctx context.Context, c control.Peer, workers []*leapmuxv1.Worker) []doctorWorkerCheck {
	results := make([]doctorWorkerCheck, len(workers))
	var g errgroup.Group
	g.SetLimit(8)
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
				pingMS := time.Since(start).Milliseconds()
				check.PingMS = &pingMS

				var (
					infoResp  leapmuxv1.GetWorkerSystemInfoResponse
					provResp  leapmuxv1.ListAvailableProvidersResponse
					provOK    bool
					shellResp leapmuxv1.ListAvailableShellsResponse
					shellOK   bool
					inner     errgroup.Group
				)
				inner.Go(func() error {
					if err := call.Call(ctx, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, &infoResp); err != nil {
						return fmt.Errorf("system info failed: %w", err)
					}
					return nil
				})
				inner.Go(func() error {
					if err := call.Call(ctx, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, &leapmuxv1.ListAvailableProvidersRequest{}, &provResp); err == nil {
						provOK = true
					}
					return nil
				})
				inner.Go(func() error {
					if err := call.Call(ctx, leapmuxv1connect.WorkerTerminalServiceListAvailableShellsProcedure, &leapmuxv1.ListAvailableShellsRequest{}, &shellResp); err == nil {
						shellOK = true
					}
					return nil
				})
				if err := inner.Wait(); err != nil {
					return err
				}
				check.Name = infoResp.GetName()
				check.Version = version.FormatBuild(infoResp.GetBuild())
				if check.Version != "" && check.Version != version.Format() {
					check.Status = "warn"
				}
				if provOK {
					n := len(provResp.GetProviders())
					check.Providers = &n
				}
				if shellOK {
					n := len(shellResp.GetShells())
					check.Shells = &n
				}
				return nil
			})
			if probeErr != nil {
				check.Status = "fail"
				check.Error = probeErr.Error()
			}
			results[idx] = check
			return nil
		})
	}
	_ = g.Wait()
	return results
}
