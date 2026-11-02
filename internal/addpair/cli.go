package addpair

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"crsf2/internal/awgservice"
)

// Run validates command arguments, adds one Radio/Module pair, and updates the gateway.
func Run(ctx context.Context, args []string, output, errorOutput io.Writer) int {
	if len(args) != 4 {
		fmt.Fprintln(errorOutput, "Usage: go run ./cmd/add-pair <name> <radio-lan-cidr> <module-lan-cidr> <vds-endpoint:port>")
		return 2
	}
	radioLAN, err := parseIPv4Network(args[1], "Radio LAN")
	if err != nil {
		fmt.Fprintln(errorOutput, "Error:", err)
		return 2
	}
	moduleLAN, err := parseIPv4Network(args[2], "Module LAN")
	if err != nil {
		fmt.Fprintln(errorOutput, "Error:", err)
		return 2
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(errorOutput, "Error: get project directory:", err)
		return 1
	}

	manager := newPairManager(root)
	pairPath, err := manager.add(ctx, pairOptions{
		name:      args[0],
		radioLAN:  radioLAN,
		moduleLAN: moduleLAN,
		endpoint:  args[3],
	})
	if err != nil {
		fmt.Fprintln(errorOutput, "Error:", err)
		return 1
	}
	fmt.Fprintf(output, "Created client profiles: %s and %s\n",
		filepath.Join(pairPath, "radio.conf"), filepath.Join(pairPath, "module.conf"))
	fmt.Fprintln(output, "Each profile contains a private key; transfer it only to its matching gateway.")

	restarted, err := awgservice.New(root).RestartIfActive(ctx, output)
	if err != nil {
		fmt.Fprintln(errorOutput, "Pair was added, but gateway update failed:", err)
		fmt.Fprintln(errorOutput, "After resolving the error, apply the config with: sudo systemctl restart crsf2-amneziawg")
		return 1
	}
	if restarted {
		fmt.Fprintln(output, "Gateway restarted and is healthy with the new peers.")
	} else {
		fmt.Fprintln(output, "Gateway is not running. Deploy or start it with: sudo go run ./cmd/deploy-server")
	}
	return 0
}
