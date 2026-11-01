package setupserver

import (
	"context"
	"io"
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

// Run starts the interactive first-time server setup in root.
func Run(ctx context.Context, root string, input io.Reader, output io.Writer) error {
	wizard := newSetupWizard(ctx, root, input, output)
	return wizard.run()
}
