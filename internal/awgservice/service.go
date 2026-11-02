package awgservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const serviceName = "crsf2-amneziawg.service"

type Manager struct {
	root string
}

func New(root string) Manager {
	return Manager{root: root}
}

func (manager Manager) Install(ctx context.Context, output io.Writer) error {
	script := filepath.Join(manager.root, "scripts", "install-amneziawg.sh")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("AmneziaWG installer is missing: %w", err)
	}
	return manager.interactive(ctx, output, "bash", script)
}

func (manager Manager) StopIfActive(ctx context.Context, output io.Writer) error {
	active, err := manager.isActive(ctx)
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	fmt.Fprintf(output, "Stopping %s before replacing the configuration...\n", serviceName)
	return manager.interactive(ctx, output, "systemctl", "stop", serviceName)
}

func (manager Manager) StartAndWait(ctx context.Context, output io.Writer) error {
	if err := manager.interactive(ctx, output, "systemctl", "enable", "--now", serviceName); err != nil {
		return manager.withJournal(ctx, fmt.Errorf("enable and start %s: %w", serviceName, err))
	}
	return manager.WaitUntilReady(ctx)
}

func (manager Manager) RestartIfActive(ctx context.Context, output io.Writer) (bool, error) {
	active, err := manager.isActive(ctx)
	if err != nil {
		return false, err
	}
	if !active {
		return false, nil
	}
	if err := manager.interactive(ctx, output, "systemctl", "restart", serviceName); err != nil {
		return false, manager.withJournal(ctx, fmt.Errorf("restart %s: %w", serviceName, err))
	}
	if err := manager.WaitUntilReady(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (manager Manager) WaitUntilReady(ctx context.Context) error {
	var lastCheckErr error
	for range 60 {
		state, err := manager.output(ctx, "systemctl", "show", "--property=ActiveState", "--value", serviceName)
		if err != nil {
			return fmt.Errorf("check %s state: %w", serviceName, err)
		}
		switch strings.TrimSpace(state) {
		case "active":
			if _, err := manager.output(ctx, "ip", "link", "show", "dev", "awg0"); err != nil {
				lastCheckErr = fmt.Errorf("check awg0 interface: %w", err)
				break
			}
			if _, err := manager.output(ctx, "awg", "show", "awg0"); err != nil {
				lastCheckErr = fmt.Errorf("check awg0 configuration: %w", err)
				break
			}
			return nil
		case "failed":
			return manager.withJournal(ctx, errors.New("AmneziaWG service entered failed state"))
		case "inactive":
			return manager.withJournal(ctx, errors.New("AmneziaWG service is inactive"))
		}
		if err := wait(ctx, time.Second); err != nil {
			return err
		}
	}
	cause := errors.New("timed out waiting for AmneziaWG service to become ready")
	if lastCheckErr != nil {
		cause = errors.Join(cause, lastCheckErr)
	}
	return manager.withJournal(ctx, cause)
}

func (manager Manager) isActive(ctx context.Context) (bool, error) {
	loadState, err := manager.output(ctx, "systemctl", "show", "--property=LoadState", "--value", serviceName)
	if err != nil {
		return false, fmt.Errorf("check whether %s is installed: %w", serviceName, err)
	}
	if strings.TrimSpace(loadState) == "not-found" {
		return false, nil
	}
	activeState, err := manager.output(ctx, "systemctl", "show", "--property=ActiveState", "--value", serviceName)
	if err != nil {
		return false, fmt.Errorf("check %s activity: %w", serviceName, err)
	}
	return strings.TrimSpace(activeState) == "active", nil
}

func (manager Manager) withJournal(ctx context.Context, cause error) error {
	logs, err := manager.output(ctx, "journalctl", "--no-pager", "-u", serviceName, "-n", "50")
	if err != nil {
		return errors.Join(cause, fmt.Errorf("read service journal: %w", err))
	}
	if logs == "" {
		return cause
	}
	return fmt.Errorf("%w\n%s", cause, logs)
}

func (manager Manager) output(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = manager.root
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("%s: %s: %w", name, detail, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (manager Manager) interactive(ctx context.Context, output io.Writer, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = manager.root
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s canceled: %w", name, ctxErr)
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func wait(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
