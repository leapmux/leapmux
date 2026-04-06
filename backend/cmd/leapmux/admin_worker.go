package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

func runAdminWorker(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: leapmux admin worker <command> [flags]\n\nCommands:\n  list              List workers\n  get               Get worker details\n  deregister        Deregister a worker")
	}

	switch args[0] {
	case "list":
		return runWorkerList(args[1:])
	case "get":
		return runWorkerGet(args[1:])
	case "deregister":
		return runWorkerDeregister(args[1:])
	default:
		return fmt.Errorf("unknown worker command: %s", args[0])
	}
}

func runWorkerList(args []string) error {
	var userID *string
	var username *string
	var status *string
	var limit *int64
	var cursor *string
	return withAdminStore("worker list", args, func(fs *flag.FlagSet) {
		userID = fs.String("user-id", "", "filter by user ID")
		username = fs.String("username", "", "filter by username")
		status = fs.String("status", "active", "filter by status (active, deregistering, deleted, all)")
		limit = fs.Int64("limit", 50, "maximum number of results")
		cursor = fs.String("cursor", "", "cursor for pagination (created_at in RFC3339Nano)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		var resolvedUserID *string
		if *userID != "" {
			resolvedUserID = userID
		} else if *username != "" {
			user, err := st.Users().GetByUsername(ctx, *username)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("user not found: %s", *username)
				}
				return fmt.Errorf("get user: %w", err)
			}
			resolvedUserID = &user.ID
		}

		allStatuses := *status == "all"
		var statusVal *leapmuxv1.WorkerStatus
		if !allStatuses {
			s, parseErr := parseWorkerStatus(*status)
			if parseErr != nil {
				return parseErr
			}
			statusVal = &s
		}

		rows, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			UserID: resolvedUserID,
			Status: statusVal,
			Cursor: *cursor,
			Limit:  *limit,
		})
		if err != nil {
			return fmt.Errorf("list workers: %w", err)
		}

		if len(rows) == 0 {
			fmt.Println("No workers found.")
			return nil
		}

		fmt.Printf("%-48s %-20s %-16s %-24s %-24s\n", "ID", "OWNER", "STATUS", "CREATED", "LAST_SEEN")
		for _, w := range rows {
			lastSeen := "-"
			if w.LastSeenAt != nil {
				lastSeen = timefmt.Format(*w.LastSeenAt)
			}
			fmt.Printf("%-48s %-20s %-16s %-24s %-24s\n",
				w.ID, w.OwnerUsername, workerStatusString(w.Status), timefmt.Format(w.CreatedAt), lastSeen)
		}

		maybePrintNextCursor(rows, *limit, func(w store.WorkerWithOwner) time.Time { return w.CreatedAt })
		return nil
	})
}

func runWorkerGet(args []string) error {
	var workerID *string
	return withAdminStore("worker get", args, func(fs *flag.FlagSet) {
		workerID = fs.String("id", "", "worker ID (required)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if *workerID == "" {
			return fmt.Errorf("--id is required")
		}

		worker, err := st.Workers().GetByID(ctx, *workerID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("worker not found: %s", *workerID)
			}
			return fmt.Errorf("get worker: %w", err)
		}

		lastSeen := "-"
		if worker.LastSeenAt != nil {
			lastSeen = timefmt.Format(*worker.LastSeenAt)
		}

		fmt.Printf("ID:              %s\n", worker.ID)
		fmt.Printf("Registered by:   %s\n", worker.RegisteredBy)
		fmt.Printf("Status:          %s\n", workerStatusString(worker.Status))
		fmt.Printf("Created at:      %s\n", timefmt.Format(worker.CreatedAt))
		fmt.Printf("Last seen at:    %s\n", lastSeen)

		// Show access grants.
		grants, err := st.WorkerAccessGrants().List(ctx, *workerID)
		if err != nil {
			return fmt.Errorf("list access grants: %w", err)
		}

		if len(grants) > 0 {
			fmt.Println("\nAccess grants:")
			fmt.Printf("  %-48s %-48s %-24s\n", "USER_ID", "GRANTED_BY", "CREATED")
			for _, g := range grants {
				fmt.Printf("  %-48s %-48s %-24s\n", g.UserID, g.GrantedBy, timefmt.Format(g.CreatedAt))
			}
		} else {
			fmt.Println("\nNo access grants.")
		}

		return nil
	})
}

func runWorkerDeregister(args []string) error {
	var workerID *string
	return withAdminStore("worker deregister", args, func(fs *flag.FlagSet) {
		workerID = fs.String("id", "", "worker ID (required)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		if *workerID == "" {
			return fmt.Errorf("--id is required")
		}

		n, err := st.Workers().ForceDeregister(ctx, *workerID)
		if err != nil {
			return fmt.Errorf("deregister worker: %w", err)
		}

		if n == 0 {
			return fmt.Errorf("worker %s not found or not active", *workerID)
		}

		fmt.Printf("Deregistered worker %s\n", *workerID)
		return nil
	})
}

func parseWorkerStatus(s string) (leapmuxv1.WorkerStatus, error) {
	switch strings.ToLower(s) {
	case "active":
		return leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE, nil
	case "deregistering":
		return leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING, nil
	case "deleted":
		return leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED, nil
	default:
		return 0, fmt.Errorf("unknown worker status: %s (use: active, deregistering, deleted, all)", s)
	}
}

func workerStatusString(s leapmuxv1.WorkerStatus) string {
	switch s {
	case leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE:
		return "active"
	case leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING:
		return "deregistering"
	case leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED:
		return "deleted"
	default:
		return "unknown"
	}
}
