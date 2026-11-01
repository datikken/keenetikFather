package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRadioLAN      = "192.168.66.0/24"
	defaultModuleLAN     = "192.168.55.0/24"
	serverAddress        = "10.77.0.1/24"
	defaultServerUDPPort = 51820
	firstPairName        = "first"
	imageName            = "crsf2-amneziawg:local"
	backupDirName        = "setup-backups"
)

type setupInput struct {
	endpoint   string
	serverPort int
	radioLAN   net.IPNet
	moduleLAN  net.IPNet
}

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := runSetup(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		return 1
	}
	return 0
}

func runSetup(ctx context.Context, input io.Reader, output io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("определить каталог проекта: %w", err)
	}
	if _, err := os.Stat(filepath.Join(root, "compose.yaml")); err != nil {
		return fmt.Errorf("запустите команду из каталога проекта (compose.yaml не найден): %w", err)
	}

	serverConfigPath := filepath.Join(root, "config", "server.conf")
	configDir := filepath.Dir(serverConfigPath)
	lockPath := filepath.Join(configDir, ".add-pair.lock")
	lockExists, err := fileExists(lockPath)
	if err != nil {
		return fmt.Errorf("проверить блокировку добавления пары: %w", err)
	}
	if lockExists {
		return errors.New("обнаружен config/.add-pair.lock: дождитесь завершения добавления пары и запустите настройку снова")
	}

	values, err := promptSetup(input, output)
	if err != nil {
		return err
	}

	fmt.Fprintln(output, "\nСобираю Docker-образ...")
	if err := runInteractive(ctx, root, output, "docker", "compose", "build"); err != nil {
		return fmt.Errorf("сборка Docker-образа: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := stopExistingService(ctx, root, output); err != nil {
		return fmt.Errorf("остановка существующего сервера: %w", err)
	}
	backupPath, err := archiveExistingConfig(root, time.Now())
	if err != nil {
		return fmt.Errorf("резервное копирование старой конфигурации: %w", err)
	}
	if backupPath != "" {
		fmt.Fprintf(output, "\nСтарая конфигурация сохранена в %s\n", backupPath)
		fmt.Fprintln(output, "Создаю новую конфигурацию с нуля...")
	}

	fmt.Fprintln(output, "\nСоздаю ключ сервера...")
	serverPrivateKey, err := commandOutput(ctx, root, "docker", "run", "--rm", "--network", "none",
		"--entrypoint", "awg", imageName, "genkey")
	if err != nil {
		return fmt.Errorf("генерация ключа сервера: %w", err)
	}
	if !isAWGKey(serverPrivateKey) {
		return errors.New("awg вернул некорректный ключ сервера")
	}
	if err := writePrivateFile(serverConfigPath, initialServerConfig(serverPrivateKey, values.serverPort)); err != nil {
		return fmt.Errorf("создание config/server.conf: %w", err)
	}

	fmt.Fprintln(output, "\nСоздаю клиентские профили Radio и Module...")
	if err := runInteractive(ctx, root, output, "go", "run", "./cmd/add-pair", firstPairName,
		values.radioLAN.String(), values.moduleLAN.String(), values.endpoint); err != nil {
		return fmt.Errorf("создание первой пары: %w", err)
	}

	fmt.Fprintln(output, "\nЗапускаю сервер...")
	if err := runInteractive(ctx, root, output, "docker", "compose", "up", "-d", "--force-recreate"); err != nil {
		return fmt.Errorf("запуск сервера: %w", err)
	}
	if err := waitUntilHealthy(ctx, root); err != nil {
		return err
	}

	fmt.Fprintln(output, "\nСервер запущен и прошёл healthcheck.")
	fmt.Fprintf(output, "Radio LAN:  %s\n", values.radioLAN.String())
	fmt.Fprintf(output, "Module LAN: %s\n", values.moduleLAN.String())
	fmt.Fprintf(output, "Публичный endpoint: %s\n", values.endpoint)
	fmt.Fprintf(output, "Профили созданы в config/pairs/%s/.\n", firstPairName)
	fmt.Fprintln(output, "Передайте radio.conf шлюзу Radio, а module.conf — шлюзу Module.")
	return nil
}

func promptSetup(input io.Reader, output io.Writer) (setupInput, error) {
	reader := bufio.NewReader(input)
	endpoint, err := promptPublicIPv4(reader, output)
	if err != nil {
		return setupInput{}, err
	}
	serverPort, err := promptServerUDPPort(reader, output, defaultServerUDPPort)
	if err != nil {
		return setupInput{}, err
	}
	radioLAN, err := promptLAN(reader, output, "LAN Radio", defaultRadioLAN)
	if err != nil {
		return setupInput{}, err
	}
	moduleLAN, err := promptLAN(reader, output, "LAN Module", defaultModuleLAN)
	if err != nil {
		return setupInput{}, err
	}
	if networksOverlap(radioLAN, moduleLAN) {
		return setupInput{}, errors.New("LAN Radio и LAN Module не должны пересекаться")
	}
	_, vpnNetwork, _ := net.ParseCIDR(serverAddress)
	if networksOverlap(radioLAN, *vpnNetwork) || networksOverlap(moduleLAN, *vpnNetwork) {
		return setupInput{}, fmt.Errorf("LAN-подсети не должны пересекаться с VPN-сетью %s", vpnNetwork)
	}
	return setupInput{
		endpoint:   net.JoinHostPort(endpoint, strconv.Itoa(serverPort)),
		serverPort: serverPort,
		radioLAN:   radioLAN,
		moduleLAN:  moduleLAN,
	}, nil
}

func promptPublicIPv4(reader *bufio.Reader, output io.Writer) (string, error) {
	for {
		fmt.Fprint(output, "Публичный IPv4-адрес этого сервера: ")
		value, err := readLine(reader)
		if err != nil {
			return "", err
		}
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil || !isPublicIPv4(ip.To4()) {
			fmt.Fprintln(output, "Введите публичный IPv4-адрес, доступный клиентам из интернета.")
			continue
		}
		return ip.To4().String(), nil
	}
}

func promptServerUDPPort(reader *bufio.Reader, output io.Writer, defaultPort int) (int, error) {
	return promptServerUDPPortWithCheck(reader, output, defaultPort, isUDPPortAvailable)
}

func promptServerUDPPortWithCheck(reader *bufio.Reader, output io.Writer, defaultPort int, check func(int) (bool, error)) (int, error) {
	for {
		fmt.Fprintf(output, "UDP-порт сервера [%d]: ", defaultPort)
		value, err := readLine(reader)
		if err != nil {
			return 0, err
		}
		port := defaultPort
		if value != "" {
			validDigits := true
			for _, character := range value {
				if character < '0' || character > '9' {
					validDigits = false
					break
				}
			}
			if !validDigits {
				fmt.Fprintln(output, "Введите номер UDP-порта от 1 до 65535.")
				continue
			}
			port, err = strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				fmt.Fprintln(output, "Введите номер UDP-порта от 1 до 65535.")
				continue
			}
		}
		available, err := check(port)
		if err != nil {
			return 0, fmt.Errorf("проверить UDP-порт %d: %w", port, err)
		}
		if !available {
			fmt.Fprintf(output, "UDP-порт %d уже используется. Остановите использующий его сервис и введите порт снова.\n", port)
			continue
		}
		return port, nil
	}
}

func isUDPPortAvailable(port int) (bool, error) {
	address := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	listener, err := net.ListenPacket("udp4", address)
	if err != nil {
		if isAddressInUse(err) {
			return false, nil
		}
		return false, err
	}
	if err := listener.Close(); err != nil {
		return false, fmt.Errorf("закрыть проверочный UDP-сокет: %w", err)
	}
	return true, nil
}

func isPublicIPv4(ip net.IP) bool {
	if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() ||
		ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}

	for _, block := range []string{
		"100.64.0.0/10",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"192.88.99.0/24",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"240.0.0.0/4",
	} {
		_, network, err := net.ParseCIDR(block)
		if err != nil {
			return false
		}
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

func networksOverlap(first, second net.IPNet) bool {
	return first.Contains(second.IP) || second.Contains(first.IP)
}

func promptLAN(reader *bufio.Reader, output io.Writer, label, defaultValue string) (net.IPNet, error) {
	for {
		fmt.Fprintf(output, "%s в CIDR [%s]: ", label, defaultValue)
		value, err := readLine(reader)
		if err != nil {
			return net.IPNet{}, err
		}
		if value == "" {
			value = defaultValue
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil || network == nil || network.IP.To4() == nil {
			fmt.Fprintln(output, "Введите IPv4-подсеть в CIDR, например 192.168.66.0/24.")
			continue
		}
		ip := net.ParseIP(value[:strings.Index(value, "/")])
		if ip == nil || !ip.Equal(network.IP) {
			fmt.Fprintln(output, "Укажите адрес сети, например 192.168.66.0/24, а не адрес устройства.")
			continue
		}
		network.IP = network.IP.To4()
		return *network, nil
	}
}

func readLine(reader *bufio.Reader) (string, error) {
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("прочитать ввод: %w", err)
	}
	if errors.Is(err, io.EOF) && value == "" {
		return "", errors.New("ввод завершён до заполнения параметров")
	}
	return strings.TrimSpace(value), nil
}

func initialServerConfig(privateKey string, listenPort int) string {
	return fmt.Sprintf(`[Interface]
Address = %s
ListenPort = %d
PrivateKey = %s

# These example AWG values are copied to both peers.
Jc = 5
Jmin = 10
Jmax = 50
S1 = 0
S2 = 0
H1 = 1
H2 = 2
H3 = 3
H4 = 4
`, serverAddress, listenPort, privateKey)
}

func isAWGKey(value string) bool {
	if len(value) != 44 || value[43] != '=' {
		return false
	}
	for _, character := range value[:43] {
		if !(character >= 'A' && character <= 'Z') &&
			!(character >= 'a' && character <= 'z') &&
			!(character >= '0' && character <= '9') &&
			character != '+' && character != '/' {
			return false
		}
	}
	return true
}

func writePrivateFile(path, contents string) (returnErr error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s уже существует; отказываюсь его перезаписывать", path)
		}
		return err
	}
	closed := false
	committed := false
	defer func() {
		if !closed {
			if err := file.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close private file: %w", err))
			}
		}
		if !committed {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove incomplete private file: %w", err))
			}
		}
	}()
	if err := file.Chmod(0600); err != nil {
		return fmt.Errorf("set private file permissions: %w", err)
	}
	if _, err := file.WriteString(contents); err != nil {
		return fmt.Errorf("write private file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync private file: %w", err)
	}
	if err := file.Close(); err != nil {
		closed = true
		return fmt.Errorf("close private file: %w", err)
	}
	closed = true
	committed = true
	return nil
}

func commandOutput(ctx context.Context, root, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = root
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

func runInteractive(ctx context.Context, root string, output io.Writer, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = root
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

func stopExistingService(ctx context.Context, root string, output io.Writer) error {
	containerID, err := commandOutput(ctx, root, "docker", "compose", "ps", "-q", "amneziawg")
	if err != nil {
		return err
	}
	if containerID == "" {
		return nil
	}
	fmt.Fprintln(output, "\nОстанавливаю существующий сервер перед сбросом...")
	return runInteractive(ctx, root, output, "docker", "compose", "stop", "amneziawg")
}

func archiveExistingConfig(root string, now time.Time) (backupPath string, returnErr error) {
	configDir := filepath.Join(root, "config")
	backupRoot := filepath.Join(configDir, backupDirName)
	paths := []string{
		filepath.Join(configDir, "server.conf"),
		filepath.Join(configDir, "radio-peer.conf"),
		filepath.Join(configDir, "module-peer.conf"),
		filepath.Join(configDir, "pairs"),
	}
	var existing []string
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			existing = append(existing, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("проверить %s: %w", path, err)
		}
	}
	if len(existing) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(backupRoot, 0700); err != nil {
		return "", fmt.Errorf("создать каталог резервных копий: %w", err)
	}
	if err := os.Chmod(backupRoot, 0700); err != nil {
		return "", fmt.Errorf("ограничить доступ к резервным копиям: %w", err)
	}
	backupPath = filepath.Join(backupRoot, now.Format("20060102-150405.000000000"))
	if err := os.Mkdir(backupPath, 0700); err != nil {
		return "", fmt.Errorf("создать резервную копию %s: %w", backupPath, err)
	}
	type movedPath struct {
		source string
		target string
	}
	var moved []movedPath
	for _, path := range existing {
		target := filepath.Join(backupPath, filepath.Base(path))
		if err := os.Rename(path, target); err != nil {
			returnErr = fmt.Errorf("переместить %s в резервную копию %s: %w", path, backupPath, err)
			for index := len(moved) - 1; index >= 0; index-- {
				if rollbackErr := os.Rename(moved[index].target, moved[index].source); rollbackErr != nil {
					returnErr = errors.Join(returnErr, fmt.Errorf("восстановить %s: %w", moved[index].source, rollbackErr))
				}
			}
			if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("удалить неполную резервную копию %s: %w", backupPath, err))
			}
			if returnErr != nil {
				return backupPath, returnErr
			}
		}
		moved = append(moved, movedPath{source: path, target: target})
	}
	return backupPath, nil
}

func waitUntilHealthy(ctx context.Context, root string) error {
	containerID, err := commandOutput(ctx, root, "docker", "compose", "ps", "-q", "amneziawg")
	if err != nil {
		return fmt.Errorf("получить состояние контейнера: %w", err)
	}
	if containerID == "" {
		return errors.New("Docker Compose не создал контейнер amneziawg")
	}
	for range 90 {
		status, err := commandOutput(ctx, root, "docker", "inspect", "--format",
			"{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", containerID)
		if err != nil {
			return fmt.Errorf("проверить состояние контейнера: %w", err)
		}
		if strings.HasSuffix(status, "|healthy") {
			return nil
		}
		if strings.HasPrefix(status, "exited|") || strings.HasPrefix(status, "dead|") {
			return showFailureLogs(ctx, root, fmt.Errorf("сервер завершился со статусом %s; проверьте docker compose logs amneziawg", status))
		}
		if strings.HasSuffix(status, "|unhealthy") {
			return showFailureLogs(ctx, root, errors.New("сервер не прошёл healthcheck; проверьте docker compose logs amneziawg"))
		}
		if err := waitForNextPoll(ctx, time.Second); err != nil {
			return err
		}
	}
	return errors.New("таймаут ожидания healthcheck; проверьте docker compose ps и docker compose logs amneziawg")
}

func showFailureLogs(ctx context.Context, root string, cause error) error {
	if err := runInteractive(ctx, root, os.Stderr, "docker", "compose", "logs", "--tail=50", "amneziawg"); err != nil {
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

func fileExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
