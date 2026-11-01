package setupserver

import "fmt"

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
