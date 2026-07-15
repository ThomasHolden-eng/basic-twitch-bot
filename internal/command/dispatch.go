package command

import (
	"fmt"
	"strings"
	"time"

	"twitchbotv2/internal/chat"
	"twitchbotv2/internal/state"
)

// requireConfig returns a non-nil *state.SafeConfig or an error. Use
// this in commands that read config fields.
func requireConfig(h *CommandHandler) (*state.SafeConfig, error) {
	if h == nil || h.config == nil {
		return nil, fmt.Errorf("config not loaded")
	}
	return h.config, nil
}

// Each command is registered with h.Register("name", fn). The fn
// receives the *CommandHandler, the chat user, and any arguments.
func RegisterCommands(h *CommandHandler) {
	//h.Register("watchtime", watchtimeCommand)
}

// isMod checks if a user is a moderator or the broadcaster
func isMod(user chat.User) bool {
	if _, ok := user.Badges["moderator"]; ok {
		return true
	}
	if _, ok := user.Badges["broadcaster"]; ok {
		return true
	}
	return false
}

// --- commands ---

func pluralize(n int, toPluralize string) string {
	if n != 1 {
		toPluralize += "s"
	}
	return toPluralize
}

// formatDuration formats a time.Duration into a human-readable string
func formatDuration(d time.Duration) string {
	type PointerIntPair struct {
		pointer *int
		n       int
	}

	d = d.Round(time.Second)
	var parts []string

	now := time.Now().UTC()
	followedAt := now.Add(-d)

	y1, m1, d1 := now.Date()
	y2, m2, d2 := followedAt.Date()

	years := int(y1 - y2)
	months := int(m1 - m2)
	days := int(d1 - d2)
	hours := now.Hour() - followedAt.Hour()
	minutes := now.Minute() - followedAt.Minute()
	seconds := now.Second() - followedAt.Second()

	timespanList := [6]PointerIntPair{
		{&seconds, 60},
		{&minutes, 60},
		{&hours, 24},
		{&days, time.Date(y2, m2+1, 0, 0, 0, 0, 0, time.UTC).Day()},
		{&months, 12},
		{&years, 0},
	}

	// Normalises negative timespans
	for i := 0; i < len(timespanList)-1; i++ {
		if *timespanList[i].pointer < 0 {
			*timespanList[i+1].pointer--
			*timespanList[i].pointer += timespanList[i].n
		}
	}

	// Assembles the string output
	if years > 0 {
		timespan := pluralize(years, "year")
		parts = append(parts, fmt.Sprintf("%d %s", years, timespan))
	}
	if months > 0 {
		timespan := pluralize(months, "month")
		parts = append(parts, fmt.Sprintf("%d %s", months, timespan))
	}
	if days > 0 && len(parts) < 2 {
		timespan := pluralize(days, "day")
		parts = append(parts, fmt.Sprintf("%d %s", days, timespan))
	}
	if hours > 0 && len(parts) < 2 {
		timespan := pluralize(hours, "hour")
		parts = append(parts, fmt.Sprintf("%d %s", hours, timespan))
	}
	if minutes > 0 && len(parts) < 2 {
		timespan := pluralize(minutes, "minute")
		parts = append(parts, fmt.Sprintf("%d %s", minutes, timespan))
	}
	if len(parts) < 2 {
		timespan := pluralize(seconds, "second")
		parts = append(parts, fmt.Sprintf("%d %s", seconds, timespan))
	}

	return strings.Join(parts, " and ")
}
