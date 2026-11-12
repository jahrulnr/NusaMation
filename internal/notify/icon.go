package notify

import (
	"os"
	"path/filepath"

	"github.com/jahrulnr/nusamation/web"
)

// temporaryIcon supplies an absolute path usable by desktop notification APIs.
func temporaryIcon(name string) (string, func(), error) {
	data, err := web.FS().Open(name)
	if err != nil {
		return "", nil, err
	}
	defer data.Close()
	f, err := os.CreateTemp("", "nusamation-*"+filepath.Ext(name))
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err = f.ReadFrom(data); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if err = f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}
