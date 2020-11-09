package stealth

import (
	"github.com/fzxiao233/Vtb_Record/config"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestMain(m *testing.M) {
	config.PrepareConfig()

	m.Run()
}

func TestRule(t *testing.T) {
	assert := assert.New(t)
	useMain, useAlt := GetAltProxyRuleForUrl("http://d1--cn-gotcha105.bilivideo.com/live-bvc/832876/live_610390_332_c521e483/1604897258.ts?wsApp=HLS&wsMonitor=0")
	assert.Equal(useMain, 1)
	assert.Equal(useAlt, 0)
}
