package escalate

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/Pernek-Enterprises/dispatch/internal/config"
	"github.com/Pernek-Enterprises/dispatch/internal/log"
)

// Notify sends an escalation message to the human via OpenClaw.
// Uses `openclaw agent --deliver --channel <ch> --reply-to <target>`.
func Notify(cfg *config.Config, jobID, taskID, message string) error {
	channel := cfg.Notifications.Escalation
	target := cfg.Notifications.Target
	if target == "" {
		target = cfg.Notifications.Channel // backward compat
	}
	if channel == "" {
		log.Warn("No escalation channel configured — logging only")
		log.Info("ESCALATION [%s]: %s", jobID, message)
		return nil
	}

	body := fmt.Sprintf("🚨 **Dispatch Escalation**\n\n**Task:** %s\n**Job:** %s\n\n%s", taskID, jobID, message)

	args := []string{
		"agent",
		"--message", body,
		"--deliver",
		"--channel", channel,
	}
	if target != "" {
		args = append(args, "--to", target)
	}

	binary := findOpenClaw()
	log.Info("Escalating via %s → %s (target: %s)", binary, channel, target)

	cmd := exec.Command(binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Error("Escalation failed: %v\n%s", err, strings.TrimSpace(string(out)))
		return fmt.Errorf("escalation failed: %w", err)
	}

	log.Info("Escalation delivered for job %s", jobID)
	return nil
}

// NotifyReady sends a notification when a task reaches the human review step.
func NotifyReady(cfg *config.Config, jobID, taskID string) error {
	msg := fmt.Sprintf("✅ **Task ready for review**\n\nThe pipeline has completed and is waiting for your approval.\n\nRun `dispatch answer --job %s \"approved\"` to close it out.", jobID)
	return Notify(cfg, jobID, taskID, msg)
}

// NotifyFailure sends a failure notification.
func NotifyFailure(cfg *config.Config, jobID, taskID, reason string) error {
	msg := fmt.Sprintf("❌ **Job failed**\n\n%s", reason)
	return Notify(cfg, jobID, taskID, msg)
}

// NotifyMaxIterations sends a max-iterations-reached notification.
func NotifyMaxIterations(cfg *config.Config, taskID, step string, iterations int) error {
	msg := fmt.Sprintf("🔁 **Review loop exhausted** — step `%s` hit max iterations (%d).\nNeeds human decision to continue or close.", step, iterations)
	return Notify(cfg, "", taskID, msg)
}

func findOpenClaw() string {
	if p, err := exec.LookPath("openclaw"); err == nil {
		return p
	}
	candidates := []string{
		"/opt/openclaw/bin/openclaw",
		"/usr/local/bin/openclaw",
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return "openclaw"
}
