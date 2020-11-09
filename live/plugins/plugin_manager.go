package plugins

import (
	"github.com/fzxiao233/Vtb_Record/live/plugins/danmaku"
	"github.com/fzxiao233/Vtb_Record/live/plugins/redispub"
	"github.com/fzxiao233/Vtb_Record/live/plugins/webhook"
	"github.com/fzxiao233/Vtb_Record/live/videoworker"
	log "github.com/sirupsen/logrus"
	"sync"
)

type PluginCallback interface {
	PluginInit()
	LiveStart(p *videoworker.ProcessVideo) error
	DownloadStart(p *videoworker.ProcessVideo) error
	LiveEnd(p *videoworker.ProcessVideo) error
}

type PluginManager struct {
	plugins []PluginCallback
}

func (p *PluginManager) AddPlugin(plug PluginCallback) {
	plug.PluginInit()
	p.plugins = append(p.plugins, plug)
}

func (p *PluginManager) OnLiveStart(video *videoworker.ProcessVideo) {
	var wg sync.WaitGroup
	wg.Add(len(p.plugins))
	for _, plug := range p.plugins {
		go func(callback PluginCallback) {
			defer wg.Done()
			err := callback.LiveStart(video)
			if err != nil {
				video.GetLogger().Errorf("plugin %s livestart error: %s", callback, err)
			}
		}(plug)
	}
	wg.Wait()
}

func (p *PluginManager) OnDownloadStart(video *videoworker.ProcessVideo) {
	var wg sync.WaitGroup
	wg.Add(len(p.plugins))
	for _, plug := range p.plugins {
		go func(callback PluginCallback) {
			defer wg.Done()
			err := callback.DownloadStart(video)
			if err != nil {
				video.GetLogger().Errorf("plugin %s downloadstart error: %s", callback, err)
			}
		}(plug)
	}
	wg.Wait()
}

func (p *PluginManager) OnLiveEnd(video *videoworker.ProcessVideo) {
	var wg sync.WaitGroup
	wg.Add(len(p.plugins))
	for _, plug := range p.plugins {
		go func(callback PluginCallback) {
			defer wg.Done()
			err := callback.LiveEnd(video)
			if err != nil {
				log.Errorf("plugin %s liveend error: %s", callback, err)
			}
		}(plug)
	}
	wg.Wait()
}

var ManagerMutex sync.Mutex
var Manager *PluginManager

func GetPluginManager() *PluginManager {
	ManagerMutex.Lock()
	defer ManagerMutex.Unlock()
	if Manager == nil {
		pm := &PluginManager{}
		pm.AddPlugin(webhook.NewPlugin())
		pm.AddPlugin(redispub.NewPlugin())
		pm.AddPlugin(danmaku.NewPlugin())
		Manager = pm
	}
	return Manager
}
