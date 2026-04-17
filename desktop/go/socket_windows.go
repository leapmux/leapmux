//go:build windows

package main

func prepareEndpoint(pipePath string) (string, error) {
	return "npipe:" + pipePath, nil
}
