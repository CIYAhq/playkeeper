package backup

import "github.com/CIYAhq/playkeeper/internal/gamefiles"

// readProperties reads dataDir's server.properties through gamefiles: the
// game can put a link or a named pipe there, which is refused instead of
// followed or waited on.
func readProperties(dataDir string) ([]byte, error) {
	d, err := gamefiles.Open(dataDir, nil)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadProperties()
}
