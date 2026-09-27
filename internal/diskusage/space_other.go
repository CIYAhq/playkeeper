//go:build !linux

package diskusage

import "errors"

func diskSpace(_ string) (free, total int64, err error) {
	return 0, 0, errors.ErrUnsupported
}
