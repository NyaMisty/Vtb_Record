package danmaku

import (
	"fmt"
	"github.com/fzxiao233/Vtb_Record/live/plugins/base"
	"github.com/fzxiao233/Vtb_Record/live/videoworker"
	"github.com/fzxiao233/Vtb_Record/utils"
	"net/http"
	"time"
)

type DanmakuPluginConfig struct {
	Enable         bool
	DanmakuServers map[string]string
}

type PluginDanmaku struct {
	*base.PluginBase
	Config DanmakuPluginConfig
}

func (p *PluginDanmaku) PluginInit() {
	p.PluginBase.PluginInit()
}

func (p *PluginDanmaku) ReloadConfig() {
	plugin_config := DanmakuPluginConfig{}
	err := p.V.Unmarshal(&plugin_config)
	if err != nil {
		p.GetRawLogger().Printf("parse plugin plugin_config error: %s", err)
	}
	p.Config = plugin_config
}

func (p *PluginDanmaku) checkNeedRec(process *videoworker.ProcessVideo) bool {
	if p.Config.DanmakuServers == nil {
		return false
	}
	video := process.LiveStatus.Video
	if !video.UsersConfig.NeedDownload {
		return false
	}
	_, ok := p.Config.DanmakuServers[video.Provider]
	if !ok {
		return false
	}
	return true
}

func (p *PluginDanmaku) LiveStart(process *videoworker.ProcessVideo) error {
	logger := p.GetLogger(process)
	video := process.LiveStatus.Video
	if !p.checkNeedRec(process) {
		return nil
	}
	server := p.Config.DanmakuServers[video.Provider]

	client := &http.Client{}
	msgData, err := base.MakeLiveStartMsg(process)
	if err != nil {
		logger.Warn("Failed to serialize live start msg, err: %s", err)
		return nil
	}
	ret, err := utils.HttpPost(client, server+"/liveStart", map[string]string{"Content-Type": "application/json"}, msgData)
	if err != nil {
		logger.WithError(err).Warnf("Failed to start danmaku recording...")
	} else {
		logger.Infof("Started danmaku recording, server ret: %v", ret)
	}
	return nil
}

func (p *PluginDanmaku) DownloadStart(process *videoworker.ProcessVideo) error {
	if !p.checkNeedRec(process) {
		return nil
	}
	go func() {
		for {
			video := process.LiveStatus.Video
			// on DownloadStart, the DownloadDir will already be initialized, which lives until liveEnd
			dirpath := process.LiveStatus.Video.UsersConfig.DownloadDir
			filePath := utils.GenerateFilepath(dirpath, "danmaku.json")
			server := p.Config.DanmakuServers[video.Provider]

			func(server string, filePath string) {
				client := &http.Client{}
				out := utils.GetWriter(filePath)
				defer out.Close()
				var from int64
				to := time.Now().UnixNano()
				for {
					timer := time.NewTimer(time.Second * 4)
					ret, err := utils.HttpPost(client, server+fmt.Sprintf("/getDanmaku?from=%v&to=%v", from, to), map[string]string{}, []byte(video.Target))
					if err != nil {
						p.GetLogger(process).Warnf("Failed to get danmaku! err: %v", err)
					} else {
						from = to
						to = time.Now().UnixNano()
						_, err := out.Write(ret)
						if err != nil {
							p.GetLogger(process).Warnf("Error during writing to danmaku file! err: %s", err)
							break
						}
					}
					<-timer.C
					if process.Stopped {
						return
					}
				}
			}(server, filePath)
			if process.Stopped {
				return
			}
		}
	}()
	return nil
}

func (p *PluginDanmaku) LiveEnd(process *videoworker.ProcessVideo) error {
	logger := p.GetLogger(process)
	video := process.LiveStatus.Video
	if !p.checkNeedRec(process) {
		return nil
	}
	server := p.Config.DanmakuServers[video.Provider]

	client := &http.Client{}
	msgData, err := base.MakeLiveEndMsg(process)
	if err != nil {
		logger.Warn("Failed to serialize live start msg, err: %s", err)
		return nil
	}
	ret, err := utils.HttpPost(client, server+"/liveEnd", map[string]string{"Content-Type": "application/json"}, msgData)
	if err != nil {
		logger.WithError(err).Warnf("Failed to stop danmaku recording...")
	} else {
		logger.Infof("Stopped danmaku recording, server ret: %v", ret)
	}
	return nil
}

func NewPlugin() *PluginDanmaku {
	ret := &PluginDanmaku{
		PluginBase: &base.PluginBase{
			Name: "danmaku",
		},
	}
	ret.Plugin = ret
	return ret
}
