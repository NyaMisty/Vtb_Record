package stealth

import (
	"github.com/fzxiao233/Vtb_Record/config"
	re "github.com/umisama/go-regexpcache"
)

func GetAltProxyRuleForUrl(url string) (useMain int, useAlt int) {
	var rule config.AltProxyRuleEntry
	var defaultRule config.AltProxyRuleEntry
	for _, r := range config.Config.AdvancedSettings.AltProxyRule {
		if r.Pattern == "default" {
			defaultRule = r
			continue
		}
		matched, err := re.MatchString(r.Pattern, url)
		if err == nil && matched {
			rule = r
		}
	}
	if rule.Pattern == "" {
		if defaultRule.Pattern != "" {
			rule = defaultRule
		}
	}
	if rule.Pattern == "" {
		useMain = 1
		useAlt = 1
	} else {
		useMain = rule.Main
		useAlt = rule.Alt
	}
	return
}
