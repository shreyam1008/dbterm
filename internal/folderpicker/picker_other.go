//go:build !linux && !darwin && !windows

package folderpicker

import (
	"context"
	"fmt"
)

func Choose(context.Context, string) (string, error) {
	return "", fmt.Errorf("%w on this operating system; type the folder path", ErrUnavailable)
}
