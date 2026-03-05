package pipe

import (
	"bufio"
	"encoding/json"
	"os"
	"syscall"
	"time"
)

type Message struct {
	Type     string `json:"type"`
	JobID    string `json:"jobId,omitempty"`
	TaskID   string `json:"taskId,omitempty"`
	Message  string `json:"message,omitempty"`
	Question string `json:"question,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Escalate bool   `json:"escalate,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
}

// Create creates a named pipe at the given path.
func Create(path string) error {
	os.Remove(path) // clean up old pipe
	return syscall.Mkfifo(path, 0666)
}

// Listen opens the pipe and calls handler for each message.
// Re-opens on EOF to keep listening.
func Listen(path string, handler func(Message)) {
	for {
		f, err := os.Open(path)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var msg Message
			if json.Unmarshal(scanner.Bytes(), &msg) == nil {
				handler(msg)
			}
		}
		f.Close()
	}
}

// Send writes a message to the named pipe.
func Send(path string, msg Message) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return err
}
