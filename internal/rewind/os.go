package rewind

import "os"

func osEnviron() []string { return os.Environ() }

func removeFile(path string) error { return os.Remove(path) }
