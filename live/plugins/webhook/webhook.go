package webhook

import (
	"fmt"
	"github.com/fzxiao233/Vtb_Record/live/plugins/base"
	"github.com/fzxiao233/Vtb_Record/live/videoworker"
	"github.com/fzxiao233/Vtb_Record/utils"
	"net/http"
)

type WebhookPluginConfig struct {
	Enable         bool
	CallbackUrl    string
	CallbackHeader map[string]string
}

type PluginWebhook struct {
	*base.PluginBase
	Config WebhookPluginConfig
}

func (p *PluginWebhook) PluginInit() {
	p.PluginBase.PluginInit()
}

func (p *PluginWebhook) ReloadConfig() {
	plugin_config := WebhookPluginConfig{}
	err := p.V.Unmarshal(&plugin_config)
	if err != nil {
		p.GetRawLogger().Printf("parse plugin plugin_config error: %s", err)
	}
	p.Config = plugin_config
}

func (p *PluginWebhook) enabled(process *videoworker.ProcessVideo) bool {
	return p.Config.Enable
}

func (p *PluginWebhook) sendMsg(msgData []byte) string {
	client := &http.Client{}

	headers := map[string]string{"Content-Type": "application/json"}
	if p.Config.CallbackHeader != nil {
		for k, v := range p.Config.CallbackHeader {
			headers[k] = v
		}
	}
	_, err := utils.HttpPost(client, p.Config.CallbackUrl, headers, msgData)
	if err != nil {
		return fmt.Sprintf("Webhook callback err %s", err)
	} else {
		return fmt.Sprintf("Sent callback to %s", p.Config.CallbackUrl)
	}
}

func (p *PluginWebhook) LiveStart(process *videoworker.ProcessVideo) error {
	if !p.enabled(process) {
		return nil
	}

	msgData, err := base.MakeLiveStartMsg(process)
	if err != nil {
		p.GetLogger(process).Warnf("Failed to serialize live start msg, err: %s", err)
		return nil
	}
	ret := p.sendMsg(msgData)
	p.GetLogger(process).Infof("Sent live started, %s", ret)
	return nil
}

func (p *PluginWebhook) DownloadStart(process *videoworker.ProcessVideo) error {
	return nil
}

func (p *PluginWebhook) LiveEnd(process *videoworker.ProcessVideo) error {
	if !p.enabled(process) {
		return nil
	}
	msgData, err := base.MakeLiveEndMsg(process)
	if err != nil {
		p.GetLogger(process).Warnf("Failed to serialize live end msg, err: %s", err)
		return nil
	}
	ret := p.sendMsg(msgData)
	p.GetLogger(process).Infof("Sent live ended, %s", ret)
	return nil
}

func NewPlugin() *PluginWebhook {
	ret := &PluginWebhook{
		PluginBase: &base.PluginBase{
			Name: "webhook",
		},
	}
	ret.Plugin = ret
	return ret
}
