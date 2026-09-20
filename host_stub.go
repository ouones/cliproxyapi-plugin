//go:build !cgo

package main

import "errors"

func callHost(string, []byte) ([]byte, error) {
	return nil, errors.New("host callback is unavailable without cgo")
}

func logHost(string, string, map[string]any) {}
