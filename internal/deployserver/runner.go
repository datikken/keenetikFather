package deployserver

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"crsf2/internal/awgservice"
)

func Run(ctx context.Context, output io.Writer) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("native AmneziaWG deployment requires Linux; run this command on the VDS")
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get project directory: %w", err)
	}
	configPath := filepath.Join(root, "config", "server.conf")
	if info, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("server config %s is unavailable: %w", configPath, err)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("server config %s is not a regular file", configPath)
	}

	service := awgservice.New(root)
	fmt.Fprintln(output, "Installing AmneziaWG directly on this Linux VDS...")
	if err := service.Install(ctx, output); err != nil {
		return fmt.Errorf("install AmneziaWG: %w", err)
	}
	if err := service.StopIfActive(ctx, output); err != nil {
		return fmt.Errorf("stop existing service: %w", err)
	}
	if err := service.StartAndWait(ctx, output); err != nil {
		return fmt.Errorf("start AmneziaWG: %w", err)
	}
	fmt.Fprintln(output, "AmneziaWG is enabled and healthy as a systemd service.")
	fmt.Fprintln(output, "Check status with: systemctl status crsf2-amneziawg")
	fmt.Fprintln(output, "View logs with: journalctl -u crsf2-amneziawg -n 100 --no-pager")
	return nil
}
