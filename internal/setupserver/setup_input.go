package setupserver

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
)

type setupInput struct {
	serverIP   string
	endpoint   string
	serverPort int
	radioLAN   net.IPNet
	moduleLAN  net.IPNet
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
		serverIP:   endpoint,
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

func isAddressInUse(err error) bool {
	const windowsAddressAlreadyInUse syscall.Errno = 10048
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, windowsAddressAlreadyInUse)
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
