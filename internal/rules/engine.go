package rules

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"tgworkbench/internal/domain"
)

type Engine struct{}

func (Engine) Apply(message domain.MessageEnvelope, configured []domain.Rule) domain.TransformResult {
	result := domain.TransformResult{
		Text:     message.Text,
		Caption:  message.Caption,
		Decision: "send",
	}
	rules := append([]domain.Rule(nil), configured...)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Order < rules[j].Order })

	for _, rule := range rules {
		if !rule.Enabled || (rule.MessageType != "" && rule.MessageType != "all" && rule.MessageType != message.MessageType) {
			continue
		}
		matched, err := applyRule(&result, rule)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", rule.Name, err))
			continue
		}
		if matched {
			result.Matched = append(result.Matched, rule.ID)
		}
		if result.Decision == "drop" || result.Decision == "review" {
			break
		}
	}
	return result
}

func applyRule(result *domain.TransformResult, rule domain.Rule) (bool, error) {
	switch rule.Kind {
	case "replace", "regex_replace":
		var changed bool
		var err error
		result.Text, changed, err = replace(result.Text, rule)
		if err != nil {
			return false, err
		}
		var captionChanged bool
		result.Caption, captionChanged, err = replace(result.Caption, rule)
		return changed || captionChanged, err
	case "remove_mentions":
		return mutateBoth(result, removeMentions, rule.Replacement), nil
	case "remove_urls":
		return mutateBoth(result, regexp.MustCompile(`(?i)\b(?:https?://|t\.me/|www\.)\S+`).ReplaceAllString, rule.Replacement), nil
	case "prefix":
		if rule.Replacement == "" {
			return false, nil
		}
		result.Text = rule.Replacement + result.Text
		return true, nil
	case "suffix":
		if rule.Replacement == "" {
			return false, nil
		}
		result.Text += rule.Replacement
		return true, nil
	case "drop_if":
		matched, err := match(result.Text+"\n"+result.Caption, rule)
		if matched {
			result.Decision = "drop"
		}
		return matched, err
	case "review_if":
		matched, err := match(result.Text+"\n"+result.Caption, rule)
		if matched {
			result.Decision = "review"
		}
		return matched, err
	case "strip_forward_header":
		re := regexp.MustCompile(`(?im)^(forwarded from|转发自|来源)\s*[:：].*(\r?\n)?`)
		return mutateBoth(result, re.ReplaceAllString, ""), nil
	default:
		return false, fmt.Errorf("unsupported rule kind %q", rule.Kind)
	}
}

func removeMentions(value, replacement string) string {
	// Go's regexp engine deliberately excludes lookbehind. Preserve a possible
	// leading delimiter as a capture group instead.
	re := regexp.MustCompile(`(?i)(^|[^\pL\pN_])@[a-z0-9_]{5,32}`)
	return re.ReplaceAllString(value, `${1}`+replacement)
}

func replace(value string, rule domain.Rule) (string, bool, error) {
	if value == "" || rule.Pattern == "" {
		return value, false, nil
	}
	if rule.Kind == "regex_replace" {
		pattern := rule.Pattern
		if !rule.CaseSensitive {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return value, false, err
		}
		next := re.ReplaceAllString(value, rule.Replacement)
		return next, next != value, nil
	}
	if rule.CaseSensitive {
		next := strings.ReplaceAll(value, rule.Pattern, rule.Replacement)
		return next, next != value, nil
	}
	re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(rule.Pattern))
	if err != nil {
		return value, false, err
	}
	next := re.ReplaceAllString(value, rule.Replacement)
	return next, next != value, nil
}

func match(value string, rule domain.Rule) (bool, error) {
	pattern := rule.Pattern
	if !rule.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(value), nil
}

func mutateBoth(result *domain.TransformResult, mutate func(string, string) string, replacement string) bool {
	text := mutate(result.Text, replacement)
	caption := mutate(result.Caption, replacement)
	changed := text != result.Text || caption != result.Caption
	result.Text, result.Caption = text, caption
	return changed
}
