package config

import (
	"log"
	"os"
	"path/filepath"
	"sync"
)

var (
	appLogger *log.Logger
	logOnce   sync.Once
)

// Logger returns the shared application logger that writes to ~/.jcoding/error.log.
// It is initialised lazily and is safe for concurrent use.
func Logger() *log.Logger {
	logOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			appLogger = log.New(os.Stderr, "", log.LstdFlags)
			return
		}
		dir := filepath.Join(home, configDir)
		_ = os.MkdirAll(dir, 0o700)
		f, err := os.OpenFile(filepath.Join(dir, "error.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			appLogger = log.New(os.Stderr, "", log.LstdFlags)
			return
		}
		appLogger = log.New(f, "", log.LstdFlags)
	})
	return appLogger
}
