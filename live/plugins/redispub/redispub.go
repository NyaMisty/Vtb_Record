package redispub

import (
	"context"
	"fmt"
	"github.com/fzxiao233/Vtb_Record/live/plugins/base"
	"github.com/fzxiao233/Vtb_Record/live/videoworker"
	"github.com/go-redis/redis/v8"
)

type PluginRedisPub struct {
	*base.PluginBase
	Config		RedisPubPluginConfig
	RedisClient *redis.Client
}

type RedisPubPluginConfig struct {
	Enable           bool

	ServerAddress		string
	AuthPassword		string
	AuthUser			string
	RedisKey			string
	UseRedisStreams		bool
}

func (p *PluginRedisPub) PluginInit() {
	p.PluginBase.PluginInit()
}

func (p *PluginRedisPub) ReloadConfig() {
	logger := p.GetRawLogger()
	plugin_config := RedisPubPluginConfig{}
	err := p.V.Unmarshal(&plugin_config)
	if err != nil {
		logger.Printf("parse plugin plugin_config error: %s", err)
	}
	p.Config = plugin_config
	p.initRedis()
}

func (p *PluginRedisPub) initRedis() *redis.Client {
	if !p.Config.Enable {
		return nil
	}
	RedisClient := redis.NewClient(
		&redis.Options{
			Addr:     p.Config.ServerAddress,
			Username: p.Config.AuthUser,
			Password: p.Config.AuthPassword,
			DB:       0,
		})
	p.RedisClient = RedisClient
	return RedisClient
}

func (p *PluginRedisPub) sendMsg(data []byte) string {
	if p.Config.UseRedisStreams {
		ret := p.RedisClient.XAdd(context.Background(), &redis.XAddArgs{
			Stream:       p.Config.RedisKey,
			Values:       []interface{}{"recinfo", data},
		})
		newID, err := ret.Result()
		return fmt.Sprintf("Got newID %s, err %s", newID, err)
	} else {
		ret := p.RedisClient.Publish(context.Background(), p.Config.RedisKey, data)
		newID, err := ret.Result()
		return fmt.Sprintf("Got newID %v, err %s", newID, err)
	}
}

func (p *PluginRedisPub) enabled(process *videoworker.ProcessVideo) bool {
	if p.RedisClient == nil {
		return false
	}
	if !p.Config.Enable {
		return false
	}
	return true
}

func (p *PluginRedisPub) LiveStart(process *videoworker.ProcessVideo) error {
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

func (p *PluginRedisPub) DownloadStart(process *videoworker.ProcessVideo) error {
	return nil
}

func (p *PluginRedisPub) LiveEnd(process *videoworker.ProcessVideo) error {
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

func NewPlugin() *PluginRedisPub {
	ret := &PluginRedisPub{
		PluginBase: &base.PluginBase{
			Name:        "redispub",
		},
	}
	ret.Plugin = ret
	return ret
}
