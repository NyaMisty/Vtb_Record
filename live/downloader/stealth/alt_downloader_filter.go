package stealth

import (
	"github.com/fzxiao233/Vtb_Record/config"
	re "github.com/umisama/go-regexpcache"
)

func CheckUseAltDownloader(url string) bool {
	for _, v := range config.Config.AdvancedSettings.AltDownloaderBlacklist {
		matched, _ := re.MatchString(v, url)
		if matched {
			return false
		}
	}
	return true
}
