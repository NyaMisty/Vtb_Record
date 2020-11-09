package downloader

import (
	"github.com/fzxiao233/Vtb_Record/live/downloader/provbase"
	"github.com/fzxiao233/Vtb_Record/live/downloader/provgo"
	"github.com/fzxiao233/Vtb_Record/live/downloader/provstreamlink"
	log "github.com/sirupsen/logrus"
)

type Downloader = provbase.Downloader

func GetDownloader(providerName string) *Downloader {
	if providerName == "" || providerName == "streamlink" {
		return &Downloader{&provstreamlink.DownloaderStreamlink{}}
	} else if providerName == "go" {
		return &Downloader{&provgo.DownloaderGo{}}
	} else {
		log.Fatalf("Unknown download provider %s", providerName)
		return nil
	}
}
