//go:build !(linux || darwin || freebsd)

package offsite

import "errors"

func freeSpace(string) (int64, error) { return 0, errors.ErrUnsupported }
