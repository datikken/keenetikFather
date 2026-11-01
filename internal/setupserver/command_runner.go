package setupserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type commandRunner struct {
	root string
}

func (runner commandRunner) output(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = runner.root
	var stdout strings.Builder
	var stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("%s canceled: %w", name, ctxErr)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("%s: %s: %w", name, detail, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (runner commandRunner) interactive(ctx context.Context, output io.Writer, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = runner.root
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

type composeClient struct {
	commands commandRunner
	service  string
}

func newComposeClient(root string) composeClient {
	return composeClient{
		commands: commandRunner{root: root},
		service:  "amneziawg",
	}
}

func (client composeClient) build(ctx context.Context, output io.Writer) error {
	return client.commands.interactive(ctx, output, "docker", "compose", "build")
}

func (client composeClient) stopIfRunning(ctx context.Context, output io.Writer) error {
	containerID, err := client.commands.output(ctx, "docker", "compose", "ps", "-q", client.service)
	if err != nil {
		return err
	}
	if containerID == "" {
		return nil
	}
	fmt.Fprintln(output, "\nОстанавливаю существующий сервер перед сбросом...")
	return client.commands.interactive(ctx, output, "docker", "compose", "stop", client.service)
}

func (client composeClient) runServer(ctx context.Context, output io.Writer) error {
	return client.commands.interactive(ctx, output, "docker", "compose", "up", "-d", "--force-recreate")
}

func (client composeClient) waitUntilHealthy(ctx context.Context) error {
	containerID, err := client.commands.output(ctx, "docker", "compose", "ps", "-q", client.service)
	if err != nil {
		return fmt.Errorf("получить состояние контейнера: %w", err)
	}
	if containerID == "" {
		return errors.New("Docker Compose не создал контейнер amneziawg")
	}
	for range 90 {
		status, err := client.commands.output(ctx, "docker", "inspect", "--format",
			"{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", containerID)
		if err != nil {
			return fmt.Errorf("проверить состояние контейнера: %w", err)
		}
		if strings.HasSuffix(status, "|healthy") {
			return nil
		}
		if strings.HasPrefix(status, "exited|") || strings.HasPrefix(status, "dead|") {
			return client.showFailureLogs(ctx, fmt.Errorf("сервер завершился со статусом %s; проверьте docker compose logs amneziawg", status))
		}
		if strings.HasSuffix(status, "|unhealthy") {
			return client.showFailureLogs(ctx, errors.New("сервер не прошёл healthcheck; проверьте docker compose logs amneziawg"))
		}
		if err := waitForNextPoll(ctx, time.Second); err != nil {
			return err
		}
	}
	return errors.New("таймаут ожидания healthcheck; проверьте docker compose ps и docker compose logs amneziawg")
}

func (client composeClient) showFailureLogs(ctx context.Context, cause error) error {
	if err := client.commands.interactive(ctx, os.Stderr, "docker", "compose", "logs", "--tail=50", client.service); err != nil {
		return errors.Join(cause, fmt.Errorf("показать логи контейнера: %w", err))
	}
	return cause
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
