package site

import (
	"errors"
	"io/fs"
	"strconv"
)

func itoa(n int) string { return strconv.Itoa(n) }

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
