package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultRadioLAN  = "192.168.66.0/24"
	defaultModuleLAN = "192.168.55.0/24"
	serverAddress    = "10.77.0.1/24"
	serverListenPort = 51820
	firstPairName    = "first"
	imageName        = "crsf2-amneziawg:local"
	backupDirName    = "setup-backups"
)

type setupInput struct {
	endpoint  string
	radioLAN  net.IPNet
	moduleLAN net.IPNet
}

func main() {
	if err := runSetup(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}

func runSetup(input io.Reader, output io.Writer) error {
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
	if fileExists(lockPath) {
		return errors.New("обнаружен config/.add-pair.lock: дождитесь завершения добавления пары и запустите настройку снова")
	}

	values, err := promptSetup(input, output)
	if err != nil {
		return err
	}

	fmt.Fprintln(output, "\nСобираю Docker-образ...")
	if err := runInteractive(root, output, "docker", "compose", "build"); err != nil {
		return fmt.Errorf("сборка Docker-образа: %w", err)
	}

	if err := stopExistingService(root, output); err != nil {
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
	serverPrivateKey, err := commandOutput(root, "docker", "run", "--rm", "--network", "none",
		"--entrypoint", "awg", imageName, "genkey")
	if err != nil {
		return fmt.Errorf("генерация ключа сервера: %w", err)
	}
	if !isAWGKey(serverPrivateKey) {
		return errors.New("awg вернул некорректный ключ сервера")
	}
	if err := writePrivateFile(serverConfigPath, initialServerConfig(serverPrivateKey)); err != nil {
		return fmt.Errorf("создание config/server.conf: %w", err)
	}

	fmt.Fprintln(output, "\nСоздаю клиентские профили Radio и Module...")
	if err := runInteractive(root, output, "go", "run", "./cmd/add-pair", firstPairName,
		values.radioLAN.String(), values.moduleLAN.String(), values.endpoint); err != nil {
		return fmt.Errorf("создание первой пары: %w", err)
	}

	fmt.Fprintln(output, "\nЗапускаю сервер...")
	if err := runInteractive(root, output, "docker", "compose", "up", "-d", "--force-recreate"); err != nil {
		return fmt.Errorf("запуск сервера: %w", err)
	}
	if err := waitUntilHealthy(root); err != nil {
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
		endpoint:  net.JoinHostPort(endpoint, fmt.Sprint(serverListenPort)),
		radioLAN:  radioLAN,
		moduleLAN: moduleLAN,
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

func isPublicIPv4(ip net.IP) bool {
	return ip != nil &&
		!ip.IsPrivate() &&
		!ip.IsLoopback() &&
		!ip.IsUnspecified() &&
		!ip.IsMulticast() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast()
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

func initialServerConfig(privateKey string) string {
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
`, serverAddress, serverListenPort, privateKey)
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

func writePrivateFile(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s уже существует; отказываюсь его перезаписывать", path)
		}
		return err
	}
	if _, err := file.WriteString(contents); err != nil {
		file.Close()
		os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func commandOutput(root, name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	command.Dir = root
	var stdout strings.Builder
	var stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("%s: %s", name, detail)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runInteractive(root string, output io.Writer, name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Dir = root
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func stopExistingService(root string, output io.Writer) error {
	containerID, err := commandOutput(root, "docker", "compose", "ps", "-q", "amneziawg")
	if err != nil {
		return err
	}
	if containerID == "" {
		return nil
	}
	fmt.Fprintln(output, "\nОстанавливаю существующий сервер перед сбросом...")
	return runInteractive(root, output, "docker", "compose", "stop", "amneziawg")
}

func archiveExistingConfig(root string, now time.Time) (string, error) {
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
		} else if !os.IsNotExist(err) {
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
	backupPath := filepath.Join(backupRoot, now.Format("20060102-150405.000000000"))
	if err := os.Mkdir(backupPath, 0700); err != nil {
		return "", fmt.Errorf("создать резервную копию %s: %w", backupPath, err)
	}
	for _, path := range existing {
		if err := os.Rename(path, filepath.Join(backupPath, filepath.Base(path))); err != nil {
			return backupPath, fmt.Errorf("переместить %s в резервную копию %s: %w", path, backupPath, err)
		}
	}
	return backupPath, nil
}

func waitUntilHealthy(root string) error {
	containerID, err := commandOutput(root, "docker", "compose", "ps", "-q", "amneziawg")
	if err != nil {
		return fmt.Errorf("получить состояние контейнера: %w", err)
	}
	if containerID == "" {
		return errors.New("Docker Compose не создал контейнер amneziawg")
	}
	for range 90 {
		status, err := commandOutput(root, "docker", "inspect", "--format",
			"{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", containerID)
		if err != nil {
			return fmt.Errorf("проверить состояние контейнера: %w", err)
		}
		if strings.HasSuffix(status, "|healthy") {
			return nil
		}
		if strings.HasPrefix(status, "exited|") || strings.HasPrefix(status, "dead|") {
			_ = runInteractive(root, os.Stderr, "docker", "compose", "logs", "--tail=50", "amneziawg")
			return fmt.Errorf("сервер завершился со статусом %s; проверьте docker compose logs amneziawg", status)
		}
		if strings.HasSuffix(status, "|unhealthy") {
			_ = runInteractive(root, os.Stderr, "docker", "compose", "logs", "--tail=50", "amneziawg")
			return errors.New("сервер не прошёл healthcheck; проверьте docker compose logs amneziawg")
		}
		time.Sleep(time.Second)
	}
	return errors.New("таймаут ожидания healthcheck; проверьте docker compose ps и docker compose logs amneziawg")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
