//go:build !(linux && (amd64 || arm64))

package sync

import "errors"

func exchange(a, b string) error { return errors.New("no atomic exchange here") }
