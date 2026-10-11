//go:build unix

package kimi

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// kimiProcessListTimeout limits the process listing a stop runs.
const kimiProcessListTimeout = 2 * time.Second

// kimiDescendantGroups lists the process groups of every process under rootPID,
// excluding the worker's own group and the groups init and the kernel own. It
// reads the process table with `ps`, which every Unix the worker runs on ships
// with the same three fields.
func kimiDescendantGroups(rootPID int) []int {
	ctx, cancel := context.WithTimeout(context.Background(), kimiProcessListTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "pgid=").Output()
	if err != nil {
		return nil
	}
	return descendantGroupsFromTable(out, rootPID, syscall.Getpgrp())
}

// descendantGroupsFromTable reads a `pid ppid pgid` table and returns the
// distinct process groups of rootPID's descendants, rootPID's own included,
// minus skipGroup and every group at or below 1.
func descendantGroupsFromTable(table []byte, rootPID, skipGroup int) []int {
	children := make(map[int][]int)
	groupOf := make(map[int]int)
	scanner := bufio.NewScanner(bytes.NewReader(table))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, errParent := strconv.Atoi(fields[1])
		pgid, errGroup := strconv.Atoi(fields[2])
		if errPID != nil || errParent != nil || errGroup != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		groupOf[pid] = pgid
	}
	seen := map[int]bool{}
	var groups []int
	queue := []int{rootPID}
	visited := map[int]bool{rootPID: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if group, ok := groupOf[pid]; ok && group > 1 && group != skipGroup && !seen[group] {
			seen[group] = true
			groups = append(groups, group)
		}
		for _, child := range children[pid] {
			if !visited[child] {
				visited[child] = true
				queue = append(queue, child)
			}
		}
	}
	return groups
}

// killKimiGroups kills every group that outlived the server. A group that is
// already gone answers ESRCH, which is the outcome the stop wants.
func killKimiGroups(groups []int) {
	self := syscall.Getpgrp()
	for _, group := range groups {
		if group <= 1 || group == self {
			continue
		}
		_ = syscall.Kill(-group, syscall.SIGKILL)
	}
}
