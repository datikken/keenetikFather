package addpair

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var awgKeyPattern = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

type commandRunner struct {
	root string
}

func (runner commandRunner) run(ctx context.Context, input []byte, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = runner.root
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("command %s canceled: %w", name, ctxErr)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("command %s failed: %s: %w", name, detail, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

type awgClient struct {
	commands commandRunner
}

func newAWGClient(root string) awgClient {
	return awgClient{commands: commandRunner{root: root}}
}

func (client awgClient) generateKey(ctx context.Context) (string, error) {
	key, err := client.commands.run(ctx, nil, "awg", "genkey")
	if err != nil {
		return "", err
	}
	if !awgKeyPattern.MatchString(key) {
		return "", errors.New("awg returned an invalid private key")
	}
	return key, nil
}

func (client awgClient) publicKey(ctx context.Context, privateKey string) (string, error) {
	key, err := client.commands.run(ctx, []byte(privateKey+"\n"), "awg", "pubkey")
	if err != nil {
		return "", err
	}
	if !awgKeyPattern.MatchString(key) {
		return "", errors.New("awg returned an invalid public key")
	}
	return key, nil
}

func (client awgClient) validateConfig(ctx context.Context, configPath string) (returnErr error) {
	stagedPath, cleanup, err := stageConfigForValidation(configPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := cleanup(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove staged validation config: %w", err))
		}
	}()
	_, returnErr = client.commands.run(ctx, nil, "awg-quick", "strip", stagedPath)
	return returnErr
}

func stageConfigForValidation(configPath string) (string, func() error, error) {
	config, err := os.ReadFile(configPath)
	if err != nil {
		return "", nil, fmt.Errorf("read config for validation: %w", err)
	}
	directory, err := os.MkdirTemp("", "crsf2-awg-validation-*")
	if err != nil {
		return "", nil, fmt.Errorf("create validation directory: %w", err)
	}
	path := filepath.Join(directory, "awg0.conf")
	if err := os.WriteFile(path, config, 0600); err != nil {
		if cleanupErr := os.RemoveAll(directory); cleanupErr != nil {
			return "", nil, errors.Join(fmt.Errorf("write validation config: %w", err),
				fmt.Errorf("remove validation directory: %w", cleanupErr))
		}
		return "", nil, fmt.Errorf("write validation config: %w", err)
	}
	return path, func() error { return os.RemoveAll(directory) }, nil
}
