package setupserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type setupWizard struct {
	ctx      context.Context
	root     string
	input    io.Reader
	output   io.Writer
	compose  composeClient
	commands commandRunner
}

func newSetupWizard(ctx context.Context, root string, input io.Reader, output io.Writer) setupWizard {
	return setupWizard{
		ctx:      ctx,
		root:     root,
		input:    input,
		output:   output,
		compose:  newComposeClient(root),
		commands: commandRunner{root: root},
	}
}

func (wizard setupWizard) run() error {
	if _, err := os.Stat(filepath.Join(wizard.root, "compose.yaml")); err != nil {
		return fmt.Errorf("запустите команду из каталога проекта (compose.yaml не найден): %w", err)
	}

	lockPath := filepath.Join(wizard.root, "config", ".add-pair.lock")
	lockExists, err := fileExists(lockPath)
	if err != nil {
		return fmt.Errorf("проверить блокировку добавления пары: %w", err)
	}
	if lockExists {
		return errors.New("обнаружен config/.add-pair.lock: дождитесь завершения добавления пары и запустите настройку снова")
	}

	values, err := promptSetup(wizard.input, wizard.output)
	if err != nil {
		return err
	}
	if err := wizard.compose.build(wizard.ctx, wizard.output); err != nil {
		return fmt.Errorf("сборка Docker-образа: %w", err)
	}
	if err := wizard.ctx.Err(); err != nil {
		return err
	}
	if err := wizard.compose.stopIfRunning(wizard.ctx, wizard.output); err != nil {
		return fmt.Errorf("остановка существующего сервера: %w", err)
	}

	backupPath, err := archiveExistingConfig(wizard.root, time.Now())
	if err != nil {
		return fmt.Errorf("резервное копирование старой конфигурации: %w", err)
	}
	if backupPath != "" {
		fmt.Fprintf(wizard.output, "\nСтарая конфигурация сохранена в %s\n", backupPath)
		fmt.Fprintln(wizard.output, "Создаю новую конфигурацию с нуля...")
	}

	fmt.Fprintln(wizard.output, "\nСоздаю ключ сервера...")
	serverPrivateKey, err := wizard.commands.output(wizard.ctx, "docker", "run", "--rm", "--network", "none",
		"--entrypoint", "awg", imageName, "genkey")
	if err != nil {
		return fmt.Errorf("генерация ключа сервера: %w", err)
	}
	if !isAWGKey(serverPrivateKey) {
		return errors.New("awg вернул некорректный ключ сервера")
	}
	serverConfigPath := filepath.Join(wizard.root, "config", "server.conf")
	if err := writePrivateFile(serverConfigPath, initialServerConfig(serverPrivateKey, values.serverPort)); err != nil {
		return fmt.Errorf("создание config/server.conf: %w", err)
	}

	fmt.Fprintln(wizard.output, "\nСоздаю клиентские профили Radio и Module...")
	if err := wizard.commands.interactive(wizard.ctx, wizard.output, "go", "run", "./cmd/add-pair", firstPairName,
		values.radioLAN.String(), values.moduleLAN.String(), values.endpoint); err != nil {
		return fmt.Errorf("создание первой пары: %w", err)
	}

	fmt.Fprintln(wizard.output, "\nЗапускаю сервер...")
	if err := wizard.compose.runServer(wizard.ctx, wizard.output); err != nil {
		return fmt.Errorf("запуск сервера: %w", err)
	}
	if err := wizard.compose.waitUntilHealthy(wizard.ctx); err != nil {
		return err
	}

	fmt.Fprintln(wizard.output, "\nСервер запущен и прошёл healthcheck.")
	fmt.Fprintf(wizard.output, "Radio LAN:  %s\n", values.radioLAN.String())
	fmt.Fprintf(wizard.output, "Module LAN: %s\n", values.moduleLAN.String())
	fmt.Fprintf(wizard.output, "Публичный endpoint: %s\n", values.endpoint)
	fmt.Fprintf(wizard.output, "Профили созданы в config/pairs/%s/.\n", firstPairName)
	fmt.Fprintln(wizard.output, "Передайте radio.conf шлюзу Radio, а module.conf — шлюзу Module.")
	wizard.printClientProfileDownloadCommands(values.serverIP)
	return nil
}

func (wizard setupWizard) printClientProfileDownloadCommands(serverIP string) {
	profileDirectory := filepath.ToSlash(filepath.Join(wizard.root, "config", "pairs", firstPairName))
	fmt.Fprintln(wizard.output, "\nВыполните эти команды в PowerShell на компьютере с клиентами:")
	for _, profile := range []string{"radio.conf", "module.conf"} {
		fmt.Fprintf(wizard.output, "scp root@%s:%s/%s C:\\Users\\Motor\\Documents\\kentucky\n",
			serverIP, profileDirectory, profile)
	}
}
