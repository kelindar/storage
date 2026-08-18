package convert

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------- Title Case ----------------------------------

func Label(input string) string {
	if i := strings.LastIndex(input, "."); i >= 0 {
		input = input[i+1:]
	}

	return TitleCase(input)
}

// SlugLabel formats a provider/app slug for display (e.g. "git_hub" → "Git Hub").
func SlugLabel(slug string) string {
	return TitleCase(strings.TrimSpace(slug))
}

func TitleCase(input string) string {
	words := splitCase(input)
	smallwords := " a an on the to "
	for index, word := range words {
		if strings.Contains(smallwords, " "+word+" ") {
			words[index] = word
		} else {
			words[index] = titleWord(word)
		}
	}
	return strings.Join(words, " ")
}

func titleWord(word string) string {
	runes := []rune(word)
	if len(runes) == 0 {
		return word
	}
	runes[0] = unicode.ToTitle(runes[0])
	return string(runes)
}

// splitCase is a modified version https://github.com/fatih/camelcase
// original Copyright (c) 2015 Fatih Arslan
func splitCase(src string) (entries []string) {
	if !utf8.ValidString(src) {
		return []string{src}
	}

	entries = []string{}
	var runes [][]rune
	lastClass := 0
	class := 0

	// split into fields based on class of unicode character
	for _, r := range src {
		switch true {
		case r == '_' || r == '-':
			runes = append(runes, []rune{})
			continue
		case unicode.IsSpace(r):
			runes = append(runes, []rune{r})
			continue
		case unicode.IsLower(r):
			class = 1
		case unicode.IsUpper(r):
			class = 2
		case unicode.IsDigit(r):
			class = 3
		default:
			class = 4
		}

		if class == lastClass {
			runes[len(runes)-1] = append(runes[len(runes)-1], r)
		} else {
			runes = append(runes, []rune{r})
		}
		lastClass = class
	}

	// handle upper case -> lower case sequences, e.g.
	// "PDFL", "oader" -> "PDF", "Loader"
	for i := 0; i < len(runes)-1; i++ {
		if len(runes[i]) == 0 || len(runes[i+1]) == 0 {
			continue
		}
		if unicode.IsUpper(runes[i][0]) && unicode.IsLower(runes[i+1][0]) {
			runes[i+1] = append([]rune{runes[i][len(runes[i])-1]}, runes[i+1]...)
			runes[i] = runes[i][:len(runes[i])-1]
		}
	}

	// construct []string from results
	for _, s := range runes {
		if v := strings.Trim(string(s), " "); len(v) > 0 {
			entries = append(entries, v)
		}
	}
	return
}

// Int returns an integer value or a default value
func Int(v string, defaultValue int) int {
	if i, err := Int64(v); err == nil {
		return int(i)
	}
	return defaultValue
}

// Float returns a float value or a default value
func Float(v string, defaultValue float64) float64 {
	if f, err := Float64(v); err == nil {
		return f
	}
	return defaultValue
}

// Strings trims, drops empties, dedupes, and sorts a string list.
func Strings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func Int64(value any) (int64, error) {
	switch v := value.(type) {
	case float64:
		return int64(v), nil
	case float32:
		return int64(v), nil
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case json.Number:
		return v.Int64()
	case string:
		return strconv.ParseInt(v, 10, 64)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", value)
	}
}

func Uint64(value any) (uint64, error) {
	switch v := value.(type) {
	case float64:
		return uint64(v), nil
	case float32:
		return uint64(v), nil
	case int:
		return uint64(v), nil
	case int64:
		return uint64(v), nil
	case uint64:
		return v, nil
	case json.Number:
		i, err := v.Int64()
		return uint64(i), err
	case string:
		return strconv.ParseUint(v, 10, 64)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("cannot convert %T to uint64", value)
	}
}

func Float64(value any) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case json.Number:
		return v.Float64()
	case string:
		return strconv.ParseFloat(v, 64)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", value)
	}
}

// ---------------------------------- Schedule ----------------------------------

// ScheduleLabel returns a human-readable cron schedule label.
func ScheduleLabel(cron, timezone string) string {
	cron = strings.TrimSpace(cron)
	if cron == "" {
		return ""
	}
	fields := strings.Fields(cron)
	switch {
	case len(fields) != 5:
		return scheduleWithTimezone(cron, timezone)
	case fields[3] != "*":
		return scheduleWithTimezone(cron, timezone)
	}
	minute, hour, dayOfMonth, _, dayOfWeek := fields[0], fields[1], fields[2], fields[3], fields[4]

	var label string
	switch {
	case hour == "*" && dayOfMonth == "*" && dayOfWeek == "*" && (minute == "*" || strings.HasPrefix(minute, "*/")):
		interval := strings.TrimPrefix(minute, "*/")
		if minute == "*" || interval == "" || interval == "1" {
			label = "every minute"
		} else {
			label = fmt.Sprintf("every %s minutes", interval)
		}
	case hour == "*" && dayOfMonth == "*" && dayOfWeek == "*":
		label = fmt.Sprintf("hourly at :%s", scheduleZeroPad(minute))
	case dayOfMonth == "*" && dayOfWeek == "*":
		label = fmt.Sprintf("daily %s:%s", scheduleZeroPad(hour), scheduleZeroPad(minute))
	case dayOfMonth == "*" && dayOfWeek != "*":
		label = fmt.Sprintf("%s %s:%s", scheduleWeekday(dayOfWeek), scheduleZeroPad(hour), scheduleZeroPad(minute))
	case dayOfMonth != "*" && dayOfWeek == "*":
		label = fmt.Sprintf("monthly day %s %s:%s", dayOfMonth, scheduleZeroPad(hour), scheduleZeroPad(minute))
	default:
		label = cron
	}
	return scheduleWithTimezone(label, timezone)
}

func scheduleWithTimezone(label, timezone string) string {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return label
	}
	return label + " " + timezone
}

func scheduleZeroPad(value string) string {
	if len(value) == 1 {
		return "0" + value
	}
	return value
}

func scheduleWeekday(value string) string {
	names := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	switch value {
	case "0", "7":
		return names[0]
	case "1", "2", "3", "4", "5", "6":
		return names[value[0]-'0']
	default:
		return "weekly"
	}
}

// ---------------------------------- Builtin ID ----------------------------------

const idAlphabet = "0123456789abcdefghijklmnopqrstuv"

// BuiltinID returns the stable, tenant-specific ID for a built-in resource.
// It keeps the 20-character xid-compatible form required by URNs.
func BuiltinID(tenant, id string) string {
	sum := sha256.Sum256([]byte(tenant + "\x00" + id))
	value := make([]byte, 20)
	for i := range value {
		value[i] = idAlphabet[int(sum[i])&31]
	}
	return string(value)
}
