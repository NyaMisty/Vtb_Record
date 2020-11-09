package base

import (
	"encoding/json"
	"github.com/fsnotify/fsnotify"
	"github.com/fzxiao233/Vtb_Record/config"
	"github.com/fzxiao233/Vtb_Record/live/videoworker"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"sync"
)

type PluginBase struct {
	Name	string
	V		*viper.Viper
	ConfigMutex		  sync.Mutex
	Plugin
}

type Plugin interface {
	ReloadConfig()
}

func (p *PluginBase) GetRawLogger() *log.Entry {
	return log.WithField("plugin", p.Name)
}

func (p *PluginBase) GetLogger(process *videoworker.ProcessVideo) *log.Entry {
	return process.GetLogger().WithField("plugin", p.Name)
}

func (p *PluginBase) UpdateLogger(logger *log.Entry) *log.Entry {
	return logger.WithField("plugin", p.Name)
}


func (p *PluginBase) ReloadConfigWrap() {
	logger := p.GetRawLogger()
	p.ConfigMutex.Lock()
	defer p.ConfigMutex.Unlock()

	err := p.V.ReadInConfig()
	if err != nil {
		logger.Infof("plugin config file load error: %s, disabling", err)
		return
	}

	p.Plugin.ReloadConfig()
}

func (p *PluginBase) PluginInit() {
	v := viper.New()
	v.SetConfigName(p.Name)
	v.SetConfigType("json")
	v.AddConfigPath("pluginconf/")
	v.WatchConfig()
	v.OnConfigChange(func(in fsnotify.Event) {
		p.ReloadConfigWrap()
	})
	p.V = v
	p.ReloadConfigWrap()
}

func (p *PluginBase) LiveStart(process *videoworker.ProcessVideo) error {
	return nil
}

func (p *PluginBase) DownloadStart(process *videoworker.ProcessVideo) error {
	return nil
}

func (p *PluginBase) LiveEnd(process *videoworker.ProcessVideo) error {
	return nil
}

type LiveStartMsg struct {
	Type	 string    `json:"type"`
	Provider string    `json:"provider"`
	Title 	 string    `json:"title"`
	Target   string    `json:"target"`
	User     config.UsersConfig    `json:"user"`
}

func MakeLiveStartMsg(process *videoworker.ProcessVideo) ([]byte, error) {
	video := process.LiveStatus.Video
	msg := &LiveStartMsg{
		Type: "start",
		Provider: video.Provider,
		Title: 	  video.Title,
		Target:   video.Target,
		User:     video.UsersConfig,
	}
	return json.Marshal(msg)
}

type LiveEndMsg struct {
	Type	 string                                     `json:"type"`
	Provider string                                     `json:"provider"`
	TitleHistory 	[]videoworker.LiveTitleHistoryEntry `json:"title_history"`
	Target   string                                     `json:"target"`
	User     config.UsersConfig                         `json:"user"`
}

func MakeLiveEndMsg(process *videoworker.ProcessVideo) ([]byte, error) {
	video := process.LiveStatus.Video
	msg := &LiveEndMsg{
		Type:     "end",
		Provider: video.Provider,
		TitleHistory: 	  process.TitleHistory,
		Target:   video.Target,
		User:     video.UsersConfig,
	}
	return json.Marshal(msg)
}