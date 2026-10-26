// Package checks has health checks that most apps need.
package checks

import (
	"context"
	"fmt"
	"strings"

	health "github.com/jamesread/armature-health"
)

// DefaultPassword reports when a built-in account still has its default password
// (ID "security.default_admin_password").
type DefaultPassword struct {
	// UsesDefaultPassword reports whether the account still has the default password.
	UsesDefaultPassword func(ctx context.Context) (bool, error)
	// Product is the app's name, used in the detail text (e.g. "SickRock").
	Product string
	// Username is the account checked (default "admin"); it is also the issue key.
	Username string
	// Link is where the password can be changed (optional).
	Link string
}

func (c DefaultPassword) ID() string    { return "security.default_admin_password" }
func (c DefaultPassword) Title() string { return "Default admin password" }

func (c DefaultPassword) Run(ctx context.Context) ([]health.Issue, error) {
	usesDefault, err := c.UsesDefaultPassword(ctx)
	if err != nil || !usesDefault {
		return nil, err
	}
	username := c.Username
	if username == "" {
		username = "admin"
	}
	return []health.Issue{{
		Key:      username,
		Severity: health.SeverityError,
		Title:    fmt.Sprintf("The %q user still has the default password", username),
		Detail:   fmt.Sprintf("Anyone who can reach %s can sign in as an administrator. Change the %s password, or disable the account.", c.Product, username),
		Link:     c.Link,
	}}, nil
}

// RiskyEnvVar is an environment variable that should normally be unset.
type RiskyEnvVar struct {
	Name string
	// Severity defaults to warning.
	Severity health.Severity
	// Detail explains what the variable does and when to unset it.
	Detail string
}

// RiskySettings reports configuration that should not be left on in normal running
// (ID "config.risky_settings"): any of EnvVars that are set, and a debug or trace log level.
type RiskySettings struct {
	// Getenv reads environment variables (os.Getenv in production).
	Getenv func(string) string
	// LogLevel is the configured log level.
	LogLevel string
	// LogLevelSetting names where the log level is configured, for the detail text
	// (e.g. "SICKROCK_LOG_LEVEL / log_level").
	LogLevelSetting string
	EnvVars         []RiskyEnvVar
}

func (c RiskySettings) ID() string    { return "config.risky_settings" }
func (c RiskySettings) Title() string { return "Risky configuration settings" }

func (c RiskySettings) Run(ctx context.Context) ([]health.Issue, error) {
	var issues []health.Issue
	for _, v := range c.EnvVars {
		if c.Getenv(v.Name) != "" {
			issues = append(issues, envVarIssue(v))
		}
	}
	if level := strings.ToLower(c.LogLevel); level == "debug" || level == "trace" {
		issues = append(issues, health.Issue{
			Key:      "log-level",
			Severity: health.SeverityInfo,
			Title:    "Log level is " + level,
			Detail:   fmt.Sprintf("Verbose logging can include request details and grows log files quickly. Use info or higher in normal running (%s).", c.LogLevelSetting),
		})
	}
	return issues, nil
}

func envVarIssue(v RiskyEnvVar) health.Issue {
	severity := v.Severity
	if severity == "" {
		severity = health.SeverityWarning
	}
	return health.Issue{
		Key:      "env:" + v.Name,
		Severity: severity,
		Title:    v.Name + " is set",
		Detail:   v.Detail,
	}
}
