package invites

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/mojang"
)

// MaxWhitelistBytes bounds the whitelist.json AddToWhitelist accepts.
const MaxWhitelistBytes = 4 << 20

type whitelistEntry struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// AddToWhitelist returns a server's whitelist.json with p added, for a
// server that is stopped: a running server keeps the list in memory and
// writes it back, so it must be told with the whitelist add command
// instead. It returns false and the file unchanged if p's UUID is already
// listed. A name-only entry for p is completed with the UUID; other
// entries are kept as they are. A file that is not a list of objects is
// refused rather than overwritten.
func AddToWhitelist(file []byte, p mojang.Profile) ([]byte, bool, error) {
	id, ok := mojang.NormalizeUUID(p.ID)
	if !ok || !minecraft.ValidPlayerName(p.Name) {
		return nil, false, errors.New("the player to add needs a valid name and UUID")
	}
	if len(file) > MaxWhitelistBytes {
		return nil, false, fmt.Errorf("whitelist.json is larger than %d bytes", MaxWhitelistBytes)
	}
	var entries []json.RawMessage
	if len(bytes.TrimSpace(file)) > 0 {
		if err := json.Unmarshal(file, &entries); err != nil {
			return nil, false, errors.New("whitelist.json is not a list of players")
		}
	}
	entry, err := json.Marshal(whitelistEntry{UUID: id, Name: p.Name})
	if err != nil {
		return nil, false, err
	}
	completed := false
	for i, raw := range entries {
		var e whitelistEntry
		if json.Unmarshal(raw, &e) != nil {
			return nil, false, errors.New("whitelist.json has an entry that is not a player")
		}
		if other, ok := mojang.NormalizeUUID(e.UUID); ok && other == id {
			return file, false, nil
		}
		if e.UUID == "" && strings.EqualFold(e.Name, p.Name) && !completed {
			entries[i] = entry
			completed = true
		}
	}
	if !completed {
		entries = append(entries, entry)
	}
	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
