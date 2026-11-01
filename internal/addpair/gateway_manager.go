package addpair

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type gatewayManager struct {
	commands commandRunner
	service  string
}

func newGatewayManager(root string) gatewayManager {
	return gatewayManager{
		commands: commandRunner{root: root},
		service:  "amneziawg",
	}
}

func (manager gatewayManager) restartIfRunning(ctx context.Context) (bool, error) {
	containerID, err := manager.commands.run(ctx, nil, "docker", "compose", "ps", "-q", manager.service)
	if err != nil {
		return false, err
	}
	if containerID == "" {
		return false, nil
	}
	if _, err := manager.commands.run(ctx, nil, "docker", "compose", "restart", manager.service); err != nil {
		return false, err
	}
	for range 60 {
		status, err := manager.commands.run(ctx, nil, "docker", "inspect", "--format",
			"{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", containerID)
		if err != nil {
			return false, err
		}
		switch {
		case strings.HasSuffix(status, "|healthy"):
			return true, nil
		case strings.HasPrefix(status, "exited|"), strings.HasPrefix(status, "dead|"):
			return false, fmt.Errorf("gateway restart failed: container status %s; check docker compose logs", status)
		case strings.HasSuffix(status, "|unhealthy"):
			return false, errors.New("gateway failed its healthcheck; check docker compose logs")
		}
		if err := waitForNextPoll(ctx, time.Second); err != nil {
			return false, err
		}
	}
	return false, errors.New("timed out waiting for the restarted gateway to become healthy")
}

func waitForNextPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
