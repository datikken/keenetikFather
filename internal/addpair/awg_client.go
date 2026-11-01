package addpair

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const imageName = "crsf2-amneziawg:local"

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
	image    string
}

func newAWGClient(root string) awgClient {
	return awgClient{
		commands: commandRunner{root: root},
		image:    imageName,
	}
}

func (client awgClient) generateKey(ctx context.Context) (string, error) {
	key, err := client.commands.run(ctx, nil, "docker", "compose", "run", "--rm", "--no-deps", "-T",
		"--entrypoint", "awg", "amneziawg", "genkey")
	if err != nil {
		return "", err
	}
	if !awgKeyPattern.MatchString(key) {
		return "", errors.New("awg returned an invalid private key")
	}
	return key, nil
}

func (client awgClient) publicKey(ctx context.Context, privateKey string) (string, error) {
	key, err := client.commands.run(ctx, []byte(privateKey+"\n"), "docker", "compose", "run", "--rm", "--no-deps", "-T",
		"--entrypoint", "awg", "amneziawg", "pubkey")
	if err != nil {
		return "", err
	}
	if !awgKeyPattern.MatchString(key) {
		return "", errors.New("awg returned an invalid public key")
	}
	return key, nil
}

func (client awgClient) validateConfig(ctx context.Context, configPath string) error {
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	_, err = client.commands.run(ctx, nil, "docker", "run", "--rm", "--network", "none",
		"--mount", fmt.Sprintf("type=bind,source=%s,target=/tmp/server.conf,readonly", absolute),
		"--entrypoint", "bash", client.image, "-lc", "awg-quick strip /tmp/server.conf")
	return err
}
