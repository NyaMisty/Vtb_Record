package config

import (
	"flag"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/mitchellh/mapstructure"
	"github.com/rclone/rclone/fs"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"log"
	"os"
	"reflect"
	"strings"
)

var Config *MainConfig
var ConfigChanged bool

type UsersConfig struct {
	TargetId     string
	Name         string
	DownloadDir  string
	NeedDownload bool
	UserHeaders  map[string]string      `json:"-"`
	ExtraConfig  map[string]interface{} `json:"-"`
}
type ModuleConfig struct {
	//EnableProxy     bool
	//Proxy           string
	Name             string
	Enable           bool
	Users            []UsersConfig
	DownloadProvider string
	ExtraConfig      map[string]interface{}
}
type MainConfig struct {
	CriticalCheckSec int
	NormalCheckSec   int
	LogFile          string
	LogFileSize      int
	LogLevel         string
	FileLogLevel     string
	FluentLogLevel   string
	RLogLevel        string
	PprofHost        string
	DownloadQuality  string
	DownloadDir      []string
	UploadDir        string
	AdvancedSettings AdvancedSettings
	Module           []ModuleConfig
	EnableTS2MP4     bool
	ExtraConfig      map[string]interface{}
}

type AltProxyRuleEntry struct {
	Pattern string
	Main    int
	Alt     int
}

type AdvancedSettings struct {
	LoadBalance            []string
	LoadBalanceBlacklist   []string
	DomainRewrite          map[string]([]string)
	M3U8Rewrite            map[string]string
	GeneralRewrite         map[string]string
	AltProxyRule           []AltProxyRuleEntry
	AltDownloaderBlacklist []string
}

var v *viper.Viper

func InitConfig() {
	log.Print("Init config!")
	initConfig()
	log.Print("Load config!")
	_, _ = ReloadConfig()
	//fmt.Println(Config)
}

func initConfig() {
	/*viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.AddConfigPath("..")
	viper.AddConfigPath("../..")
	viper.SetConfigType("json")*/
	v = viper.NewWithOptions(viper.KeyDelimiter("::::"), viper.KeyPreserveCase())
	v.SetConfigFile(viper.ConfigFileUsed())
	v.WatchConfig()
	err := v.ReadInConfig()
	if err != nil {
		fmt.Printf("config file error: %s\n", err)
		os.Exit(1)
	}

	ConfigChanged = true
	v.OnConfigChange(func(in fsnotify.Event) {
		ConfigChanged = true
	})
}

func ReloadConfig() (bool, error) {
	if !ConfigChanged {
		return false, nil
	}
	ConfigChanged = false
	err := v.ReadInConfig()
	if err != nil {
		return true, err
	}
	config := &MainConfig{}
	mdMap := make(map[string]*mapstructure.Metadata, 10)
	mdMap[""] = &mapstructure.Metadata{}
	err = v.Unmarshal(config, func(c *mapstructure.DecoderConfig) {
		c.DecodeHook = mapstructure.ComposeDecodeHookFunc(
			func(inType reflect.Type, outType reflect.Type, input interface{}) (interface{}, error) {
				if inType.Kind() == reflect.Map && outType.Kind() == reflect.Struct { // we'll decoding a struct
					fieldsMap := make(map[string]reflect.StructField, 10)
					for i := 0; i < outType.NumField(); i++ {
						fieldsMap[strings.ToLower(outType.Field(i).Name)] = outType.Field(i)
					}
					inputMap := input.(map[string]interface{})
					extraConfig := make(map[string]interface{}, 5)
					inputMap["ExtraConfig"] = extraConfig
					for key := range inputMap {
						_, ok := fieldsMap[strings.ToLower(key)]
						if !ok {
							extraConfig[key] = inputMap[key]
						}
					}
				}
				return input, nil
			},
			c.DecodeHook)
	})
	if err != nil {
		fmt.Printf("Struct config error: %s", err)
	}
	/*modules := viper.AllSettings()["module"].([]interface{})
	for i := 0; i < len(modules); i++ {
		Config.Module[i].ExtraConfig = modules[i].(map[string]interface{})
	}*/
	Config = config
	UpdateLogLevel()
	return true, nil
}

func LevelStrParse(levelStr string) (level logrus.Level) {
	level = logrus.InfoLevel
	if levelStr == "debug" {
		level = logrus.DebugLevel
	} else if levelStr == "info" {
		level = logrus.InfoLevel
	} else if levelStr == "warn" {
		level = logrus.WarnLevel
	} else if levelStr == "error" {
		level = logrus.ErrorLevel
	}
	return level
}

func UpdateLogLevel() {
	fs.Config.LogLevel = fs.LogLevelInfo
	if Config.RLogLevel == "debug" {
		fs.Config.LogLevel = fs.LogLevelDebug
	} else if Config.RLogLevel == "info" {
		fs.Config.LogLevel = fs.LogLevelInfo
	} else if Config.RLogLevel == "warn" {
		fs.Config.LogLevel = fs.LogLevelWarning
	} else if Config.RLogLevel == "error" {
		fs.Config.LogLevel = fs.LogLevelError
	}
	logrus.Printf("Set rclone logrus level to %s", fs.Config.LogLevel)

	if ConsoleHook != nil {
		if Config.LogLevel == "disable" {
			ConsoleHook.Enabled = false
		} else {
			level := LevelStrParse(Config.LogLevel)
			ConsoleHook.LogLevel = level
			logrus.Printf("Set logrus console level to %s", level)
		}
	}
	if FileHook != nil {
		if Config.FileLogLevel == "disable" {
			FileHook.Enabled = false
		} else {
			level := LevelStrParse(Config.FileLogLevel)
			logrus.Printf("Set logrus file level to %s", level)
		}
	}
	if GoogleHook != nil {
		if Config.FluentLogLevel == "disable" {
			GoogleHook.Enabled = false
		} else {
			level := LevelStrParse(Config.FluentLogLevel)
			logrus.Printf("Set logrus fluentd level to %s", level)
		}
	}
}

func PrepareConfig() {
	confPath := flag.String("config", "config.json", "config.json location")
	flag.Parse()
	viper.SetConfigFile(*confPath)
	InitConfig()
}
