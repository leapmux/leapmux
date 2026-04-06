package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
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
	var offset *int64
	return withAdminDB("worker list", args, func(fs *flag.FlagSet) {
		userID = fs.String("user-id", "", "filter by user ID")
		username = fs.String("username", "", "filter by username")
		status = fs.String("status", "active", "filter by status (active, deregistering, deleted, all)")
		limit = fs.Int64("limit", 50, "maximum number of results")
		offset = fs.Int64("offset", 0, "offset for pagination")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		var resolvedUserID string
		if *userID != "" {
			resolvedUserID = *userID
		} else if *username != "" {
			user, err := q.GetUserByUsername(ctx, *username)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("user not found: %s", *username)
				}
				return fmt.Errorf("get user: %w", err)
			}
			resolvedUserID = user.ID
		}

		allStatuses := *status == "all"
		var statusVal leapmuxv1.WorkerStatus
		if !allStatuses {
			var parseErr error
			statusVal, parseErr = parseWorkerStatus(*status)
			if parseErr != nil {
				return parseErr
			}
		}

		type workerRow struct {
			ID            string
			OwnerUsername string
			Status        leapmuxv1.WorkerStatus
			CreatedAt     time.Time
			LastSeenAt    sql.NullTime
		}
		var rows []workerRow

		switch {
		case resolvedUserID != "" && !allStatuses:
			list, qErr := q.ListWorkersAdminByUserAndStatus(ctx, gendb.ListWorkersAdminByUserAndStatusParams{
				UserID: resolvedUserID, Status: statusVal, Offset: *offset, Limit: *limit,
			})
			if qErr != nil {
				return fmt.Errorf("list workers: %w", qErr)
			}
			for _, w := range list {
				rows = append(rows, workerRow{w.ID, w.OwnerUsername, w.Status, w.CreatedAt, w.LastSeenAt})
			}
		case resolvedUserID != "":
			list, qErr := q.ListWorkersAdminByUser(ctx, gendb.ListWorkersAdminByUserParams{
				UserID: resolvedUserID, Offset: *offset, Limit: *limit,
			})
			if qErr != nil {
				return fmt.Errorf("list workers: %w", qErr)
			}
			for _, w := range list {
				rows = append(rows, workerRow{w.ID, w.OwnerUsername, w.Status, w.CreatedAt, w.LastSeenAt})
			}
		case !allStatuses:
			list, qErr := q.ListWorkersAdminByStatus(ctx, gendb.ListWorkersAdminByStatusParams{
				Status: statusVal, Offset: *offset, Limit: *limit,
			})
			if qErr != nil {
				return fmt.Errorf("list workers: %w", qErr)
			}
			for _, w := range list {
				rows = append(rows, workerRow{w.ID, w.OwnerUsername, w.Status, w.CreatedAt, w.LastSeenAt})
			}
		default:
			list, qErr := q.ListWorkersAdminAll(ctx, gendb.ListWorkersAdminAllParams{
				Offset: *offset, Limit: *limit,
			})
			if qErr != nil {
				return fmt.Errorf("list workers: %w", qErr)
			}
			for _, w := range list {
				rows = append(rows, workerRow{w.ID, w.OwnerUsername, w.Status, w.CreatedAt, w.LastSeenAt})
			}
		}

		if len(rows) == 0 {
			fmt.Println("No workers found.")
			return nil
		}

		fmt.Printf("%-48s %-20s %-16s %-24s %-24s\n", "ID", "OWNER", "STATUS", "CREATED", "LAST_SEEN")
		for _, w := range rows {
			lastSeen := "-"
			if w.LastSeenAt.Valid {
				lastSeen = timefmt.Format(w.LastSeenAt.Time)
			}
			fmt.Printf("%-48s %-20s %-16s %-24s %-24s\n",
				w.ID, w.OwnerUsername, workerStatusString(w.Status), timefmt.Format(w.CreatedAt), lastSeen)
		}
		return nil
	})
}

func runWorkerGet(args []string) error {
	var workerID *string
	return withAdminDB("worker get", args, func(fs *flag.FlagSet) {
		workerID = fs.String("id", "", "worker ID (required)")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		if *workerID == "" {
			return fmt.Errorf("--id is required")
		}

		worker, err := q.GetWorkerByID(ctx, *workerID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("worker not found: %s", *workerID)
			}
			return fmt.Errorf("get worker: %w", err)
		}

		lastSeen := "-"
		if worker.LastSeenAt.Valid {
			lastSeen = timefmt.Format(worker.LastSeenAt.Time)
		}

		fmt.Printf("ID:              %s\n", worker.ID)
		fmt.Printf("Registered by:   %s\n", worker.RegisteredBy)
		fmt.Printf("Status:          %s\n", workerStatusString(worker.Status))
		fmt.Printf("Created at:      %s\n", timefmt.Format(worker.CreatedAt))
		fmt.Printf("Last seen at:    %s\n", lastSeen)

		// Show access grants.
		grants, err := q.ListWorkerAccessGrants(ctx, *workerID)
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
	return withAdminDB("worker deregister", args, func(fs *flag.FlagSet) {
		workerID = fs.String("id", "", "worker ID (required)")
	}, func(ctx context.Context, _ *config.Config, _ *sql.DB, q *gendb.Queries) error {
		if *workerID == "" {
			return fmt.Errorf("--id is required")
		}

		result, err := q.ForceDeregisterWorker(ctx, *workerID)
		if err != nil {
			return fmt.Errorf("deregister worker: %w", err)
		}

		n, _ := result.RowsAffected()
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
