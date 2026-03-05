package log

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
)

func ts() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func write(level, msg string) {
	line := fmt.Sprintf("[%s] %s: %s\n", ts(), level, msg)
	fmt.Fprint(os.Stderr, line)

	date := time.Now().Format("2006-01-02")
	logDir := filepath.Join(config.Root, "logs")
	os.MkdirAll(logDir, 0755)
	f, err := os.OpenFile(filepath.Join(logDir, date+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.WriteString(line)
		f.Close()
	}
}

func Info(msg string, args ...interface{})  { write("INFO", fmt.Sprintf(msg, args...)) }
func Warn(msg string, args ...interface{})  { write("WARN", fmt.Sprintf(msg, args...)) }
func Error(msg string, args ...interface{}) { write("ERROR", fmt.Sprintf(msg, args...)) }
