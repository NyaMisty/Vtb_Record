package main

import (
	"context"
	"fmt"
	"github.com/fzxiao233/Vtb_Record/config"
	"github.com/fzxiao233/Vtb_Record/live"
	"github.com/fzxiao233/Vtb_Record/live/downloader/stealth"
	"github.com/fzxiao233/Vtb_Record/live/monitor"
	"github.com/fzxiao233/Vtb_Record/utils"
	"github.com/rclone/rclone/fs"
	rconfig "github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/operations"
	log "github.com/sirupsen/logrus"
	"io"
	"math/rand"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

var SafeStop bool

func arrangeTask() {
	log.Printf("Arrange tasks...")
	status := make([]map[string]bool, len(config.Config.Module))
	for i, module := range config.Config.Module {
		status[i] = make(map[string]bool, len(module.Users))
		/*for j, _ := range status[i] {
			status[i][j] = false
		}*/
	}

	go func() {
		ticker := time.NewTicker(time.Second * time.Duration(1))
		for {
			if config.ConfigChanged {
				allDone := true
				/*for mod_i, _ := range status {
					for _, ch := range status[mod_i] {
						if ch != false {
							allDone = false
						}
					}
				}*/
				if allDone {
					time.Sleep(4 * time.Second) // wait to ensure the config is fully written
					rconfig.LoadConfig(context.Background())
					ret, err := config.ReloadConfig()
					if ret {
						if err == nil {
							log.Infof("\n\n\t\tConfig changed and load successfully!\n\n")
						} else {
							log.Warnf("Config changed but loading failed: %s", err)
						}
					}
				}
			}
			<-ticker.C
		}

	}()

	utils.MakeDir(config.Config.UploadDir)
	for _, dir := range config.Config.DownloadDir {
		utils.MakeDir(dir)
	}

	var statusMx sync.Mutex
	for {
		var mods []config.ModuleConfig
		living := make([]string, 0, 128)
		changed := make([]string, 0, 128)
		mods = make([]config.ModuleConfig, len(config.Config.Module))
		copy(mods, config.Config.Module)
		for mod_i, module := range mods {
			if module.Enable {
				for _, usersConfig := range module.Users {
					identifier := fmt.Sprintf("\"%s-%s\"", usersConfig.Name, usersConfig.TargetId)
					statusMx.Lock()
					if status[mod_i][identifier] != false {
						living = append(living, fmt.Sprintf("\"%s-%s\"", usersConfig.Name, usersConfig.TargetId))
						statusMx.Unlock()
						continue
					}
					status[mod_i][identifier] = true
					statusMx.Unlock()
					changed = append(changed, identifier)
					go func(i int, j string, mon monitor.VideoMonitor, userCon config.UsersConfig) {
						live.StartMonitor(mon, userCon)
						statusMx.Lock()
						status[i][j] = false
						statusMx.Unlock()
					}(mod_i, identifier, monitor.CreateVideoMonitor(module), usersConfig)
					time.Sleep(time.Millisecond * 20)
				}
			}
		}
		log.Infof("current living %s", living)
		log.Tracef("checked %s", changed)
		if time.Now().Minute() > 55 || time.Now().Minute() < 5 || (time.Now().Minute() > 25 && time.Now().Minute() < 35) {
			time.Sleep(time.Duration(config.Config.CriticalCheckSec) * time.Second)
		}
		time.Sleep(time.Duration(config.Config.NormalCheckSec) * time.Second)

		// wait all live to finish before exit :)
		if SafeStop {
			break
		}
	}
	for {
		living := make([]string, 0, 128)
		statusMx.Lock()
		for _, mod := range status {
			for name, val := range mod {
				if val {
					living = append(living, name)
				}
			}
		}
		statusMx.Unlock()
		if len(living) == 0 {
			break
		}
		log.Infof("Waiting to finish: current living %s", living)
		time.Sleep(time.Second * 30)
	}
	log.Infof("All tasks finished! Wait an additional time to ensure everything's saved")
	time.Sleep(time.Second * 300)
	log.Infof("Everything finished, exiting now~~")
}

func handleInterrupt() {
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		log.Warnf("Ctrl+C pressed in Terminal!")
		operations.RcatFiles.Range(func(key, value interface{}) bool {
			fn := key.(string)
			log.Infof("Closing opened file: %s", fn)
			in := value.(io.ReadCloser)
			in.Close()
			return true
		})
		time.Sleep(20 * time.Second) // wait rclone upload finish..
		os.Exit(0)
	}()
}

func handleUpdate() {
	c := make(chan os.Signal)
	SIGUSR1 := syscall.Signal(10)
	signal.Notify(c, SIGUSR1)
	go func() {
		<-c
		log.Warnf("Received update signal! Waiting everything done!")
		SafeStop = true
	}()
}

func main() {
	handleInterrupt()
	handleUpdate()
	fs.GetConfig(nil).StreamingUploadCutoff = fs.SizeSuffix(0)
	fs.GetConfig(nil).IgnoreChecksum = true
	fs.GetConfig(nil).NoGzip = true
	rand.Seed(time.Now().UnixNano())
	fs.GetConfig(nil).UserAgent = "google-api-go-client/0.5"

	stealth.SetupLoadBalance()

	fs.GetConfig(nil).Transfers = 20
	fs.GetConfig(nil).ConnectTimeout = time.Second * 2
	fs.GetConfig(nil).Timeout = time.Second * 4
	fs.GetConfig(nil).TPSLimit = 0
	fs.GetConfig(nil).LowLevelRetries = 120
	//fs.GetConfig(nil).NoGzip = false

	// moved to config package
	//confPath := flag.String("config", "config.json", "config.json location")
	//flag.Parse()
	//viper.SetConfigFile(*confPath)
	//config.InitConfig()
	config.PrepareConfig()

	config.InitLog()
	go config.InitProfiling()
	arrangeTask()
}
