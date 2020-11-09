package stealth

import (
	"github.com/fzxiao233/Vtb_Record/config"
	re "github.com/umisama/go-regexpcache"
	"strings"
)

type URLRewriter interface {
	Rewrite(url string) (newUrl string)
	Callback(url string, err error)
}

type BilibiliRewriter struct {
	hasErrorMap map[string]bool
}

func (u *BilibiliRewriter) Rewrite(url string) (newUrl string) {
	advSettings := config.Config.AdvancedSettings
	//onlyAlt = false
	newUrl = url
	for k, v := range advSettings.M3U8Rewrite {
		if strings.HasSuffix(k, "|onlyError") {
			if hasError, ok := u.hasErrorMap[k]; ok && hasError {
				regex, err := re.Compile(strings.TrimSuffix(k, "|onlyError"))
				if err != nil {
					continue
				}
				newUrl = regex.ReplaceAllString(newUrl, v)
			}
		} else {
			regex, err := re.Compile(k)
			if err != nil {
				continue
			}
			newUrl = regex.ReplaceAllString(newUrl, v)
		}
	}
	return
}

func (u *BilibiliRewriter) Callback(url string, err error) {
	if err != nil && strings.HasSuffix(err.Error(), "403") {
		advSettings := config.Config.AdvancedSettings
		for k, _ := range advSettings.M3U8Rewrite {
			var matched bool
			if strings.HasSuffix(k, "|onlyError") {
				matched, _ = re.MatchString(strings.TrimSuffix(k, "|onlyError"), url)
			} else {
				matched, _ = re.MatchString(k, url)
			}
			if matched {
				u.hasErrorMap[k] = true
			}
		}
	}
}

type RewriterWrap struct {
	Rewriters []URLRewriter
}

func (u *RewriterWrap) Rewrite(url string) (newUrl string) {
	for _, rewriter := range u.Rewriters {
		newUrl = rewriter.Rewrite(url)
		if newUrl != url {
			break
		}
	}
	return
}

func (u *RewriterWrap) Callback(url string, err error) {
	for _, rewriter := range u.Rewriters {
		rewriter.Callback(url, err)
	}
}

func GetRewriter() URLRewriter {
	return &RewriterWrap{
		Rewriters: []URLRewriter{
			&BilibiliRewriter{},
		},
	}
}
