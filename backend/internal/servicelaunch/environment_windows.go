package servicelaunch

import "os"

func openEnvironmentFile(path string) (*os.File, error) { return os.Open(path) }
