package youtube

import (
	"crypto/sha1"
	"fmt"
	"github.com/fzxiao233/Vtb_Record/config"
	"github.com/fzxiao233/Vtb_Record/live/interfaces"
	"github.com/fzxiao233/Vtb_Record/live/monitor/base"
	. "github.com/fzxiao233/Vtb_Record/utils"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"regexp"
	"strconv"
	"sync"
	"time"
)

type yfConfig struct {
	IsLive bool
	Title  string
	Target string
}
type Youtube struct {
	base.BaseMonitor
	yfConfig
	usersConfig config.UsersConfig
}

func getVideoInfo(ctx *base.MonitorCtx, baseHost string, channelId string) (*base.LiveInfo, error) {
	var err error
	url := baseHost + "/channel/" + channelId + "/live"
	htmlBody, err := ctx.HttpGet(url, map[string]string{})
	if err != nil {
		return nil, err
	}

	parseVideoInfo1 := func() (*gjson.Result, error) {
		re, _ := regexp.Compile(`ytplayer.config\s*=\s*([^\n]+?});`)
		htmlBody_ := string(htmlBody)
		result := re.FindStringSubmatch(htmlBody_)
		if len(result) < 2 {
			return nil, fmt.Errorf("youtube cannot find js_data")
		}
		jsonYtConfig := result[1]
		playerResponse := gjson.Get(jsonYtConfig, "args.player_response")
		if !playerResponse.Exists() {
			return nil, fmt.Errorf("youtube cannot find player_response")
		}
		playerResponse = gjson.Parse(playerResponse.String())
		return &playerResponse, nil
	}

	parseVideoInfo2 := func() (*gjson.Result, error) {
		re, _ := regexp.Compile(`var\s*ytInitialPlayerResponse\s*=\s*({[^\n]+?});var`)
		htmlBody_ := string(htmlBody)
		result := re.FindStringSubmatch(htmlBody_)
		if len(result) < 2 {
			return nil, fmt.Errorf("youtube cannot find ytInitialPlayerResponse")
		}
		jsonYtConfig := result[1]
		playerResponse := gjson.Parse(jsonYtConfig)
		if !playerResponse.Exists() {
			return nil, fmt.Errorf("youtube cannot find js_data")
		}
		return &playerResponse, nil
	}

	var playerResponse *gjson.Result
	var err1 error
	playerResponse, err1 = parseVideoInfo1()
	if err1 != nil {
		var err2 error
		playerResponse, err2 = parseVideoInfo2()
		if err2 != nil {
			return nil, fmt.Errorf("youtube getVideoInfo failed: %v %v", err1, err2)
		}
	}
	videoDetails := playerResponse.Get("videoDetails")
	if !videoDetails.Exists() {
		return nil, fmt.Errorf("youtube cannot find videoDetails")
	}
	IsLive := videoDetails.Get("isLive").Bool()
	if !IsLive {
		return nil, err
	} else {
		return &base.LiveInfo{
			Title:         videoDetails.Get("title").String(),
			StreamingLink: "https://www.youtube.com/watch?v=" + videoDetails.Get("videoId").String(),
		}, nil
	}
	//return nil, nil
	//log.Printf("%+v", y)
}

type YoutubePoller struct {
	LivingUids map[string]base.LiveInfo
	lock       sync.Mutex
}

var U2bPoller YoutubePoller

func (y *YoutubePoller) parseBaseStatus(jsonGuideData string) ([]string, error) {
	livingUids := make([]string, 0)

	addItem := func(itm gjson.Result) {
		isLive := itm.Get("guideEntryRenderer.badges.liveBroadcasting")
		if isLive.Bool() == false {
			return
		}

		browsed_id := itm.Get("guideEntryRenderer.navigationEndpoint.browseEndpoint.browseId")
		//title := itm.Get("guideEntryRenderer.title")

		livingUids = append(livingUids, browsed_id.String())
	}

	jsonParsed := gjson.Parse(jsonGuideData)
	items1 := jsonParsed.Get("items")
	for _, item := range items1.Array() {
		items2 := item.Get("guideSubscriptionsSectionRenderer.items")
		if !items2.Exists() {
			continue
		}
		for _, item2 := range items2.Array() {
			if !item2.Get("guideCollapsibleEntryRenderer").Exists() {
				addItem(item2)
			} else {
				item3 := item2.Get("guideCollapsibleEntryRenderer.expandableItems")
				for _, item4 := range item3.Array() {
					if item4.Get("guideEntryRenderer.badges").Exists() {
						addItem(item4)
					}
				}
			}
		}
	}

	log.Tracef("Parsed base uids: %s", livingUids)
	return livingUids, nil
}

func (y *YoutubePoller) parseSubscStatus(rawPage string) (map[string]base.LiveInfo, error) {
	livingUids := make(map[string]base.LiveInfo)

	// 1. var ytInitialData = {};
	// 2. window["ytInitialData"] = {};
	re, _ := regexp.Compile(`[\["\s]*ytInitialData["\]\s]*=\s*([^\n]+?});`)
	result := re.FindStringSubmatch(rawPage)
	if len(result) < 1 {
		//y.LivingUids = livingUids
		return livingUids, fmt.Errorf("youtube cannot find ytInitialData")
	}
	jsonYtConfig := result[1]
	items := gjson.Get(jsonYtConfig, "contents.twoColumnBrowseResultsRenderer.tabs.0.tabRenderer.content.sectionListRenderer.contents.0.itemSectionRenderer.contents.0.shelfRenderer.content.gridRenderer.items")
	itemArr := items.Array()
	for _, item := range itemArr {
		style := item.Get("gridVideoRenderer.badges.0.metadataBadgeRenderer.style")

		if style.String() == "BADGE_STYLE_TYPE_LIVE_NOW" {
			channelId := item.Get("gridVideoRenderer.shortBylineText.runs.0.navigationEndpoint.browseEndpoint.browseId")
			videoId := item.Get("gridVideoRenderer.videoId")
			//video_thumbnail := item.Get("gridVideoRenderer.thumbnail.thumbnails.0.url")

			//title := item.Get("gridVideoRenderer.shortBylineText.runs.0.text")
			//videoTitle := item.Get("gridVideoRenderer.title.simpleText")
			videoTitle := item.Get("gridVideoRenderer.title.runs.0.text")

			//upcomingEventData := item.Get("gridVideoRenderer.upcomingEventData")

			livingUids[channelId.String()] = base.LiveInfo{
				Title:         videoTitle.String(),
				StreamingLink: "https://www.youtube.com/watch?v=" + videoId.String(),
			}
		}

	}

	//y.LivingUids = livingUids
	log.Tracef("Parsed uids: %s", livingUids)
	return livingUids, nil
}

func (y *YoutubePoller) parseInnerTubeKey(rawPage string) (string, error) {
	re, _ := regexp.Compile(`"INNERTUBE_API_KEY":"(.*?)"`)
	result := re.FindStringSubmatch(rawPage)
	if len(result) < 2 {
		//y.LivingUids = livingUids
		return "", fmt.Errorf("youtube cannot find INNERTUBE_API_KEY")
	}
	return result[1], nil
}

func (y *YoutubePoller) getSAPISIDHASH(sid string, origin string) string {
	curTime := strconv.FormatInt(time.Now().Unix(), 10)
	payload := curTime + " " + sid + " " + origin
	return curTime + "_" + fmt.Sprintf("%x", sha1.Sum([]byte(payload)))
}

type YoutubeApiHosts struct {
	ApiHosts []string
}

func (y *YoutubePoller) getLiveStatus() error {
	var err error
	ctx := base.GetCtx("Youtube")
	//mod := interfaces.GetMod("Youtube")
	var apihosts = []string{
		"https://www.youtube.com",
	}
	apihostsConfig := YoutubeApiHosts{}
	_ = MapToStruct(ctx.ExtraModConfig, &apihostsConfig)
	if apihostsConfig.ApiHosts != nil {
		apihosts = apihostsConfig.ApiHosts
	}

	livingUids := make(map[string]base.LiveInfo)

	rawPage, err := ctx.HttpGet(
		RandChooseStr(apihosts)+"/feed/subscriptions/",
		map[string]string{})
	if err != nil {
		return err
	}
	page := string(rawPage)
	subscUids, err := y.parseSubscStatus(page)
	if err != nil {
		return err
	}
	for k, v := range subscUids {
		livingUids[k] = v
	}

	cookie, ok := ctx.GetHeaders()["Cookie"]
	if !ok {
		cookie, ok = ctx.GetHeaders()["cookie"]
		if !ok {
			return fmt.Errorf("Youtube cookie not available!?")
		}

	}
	re, _ := regexp.Compile("SAPISID=(.*?);")
	ret := re.FindStringSubmatch(cookie)
	if len(ret) < 2 {
		re, _ = regexp.Compile("__Secure-3PAPISID=(.*?);")
		ret = re.FindStringSubmatch(cookie)
	}
	if len(ret) < 2 {
		return fmt.Errorf("Youtube SAPISID not present in cookie!")
	}
	SAPISID := ret[1]

	rawPageBase, err := ctx.HttpGet(RandChooseStr(apihosts), map[string]string{})
	if err != nil {
		return err
	}
	pagebase := string(rawPageBase)
	innerKey, err := y.parseInnerTubeKey(pagebase)
	if err != nil {
		return err
	}

	rawPageBase, err = ctx.HttpPost(
		RandChooseStr(apihosts)+"/youtubei/v1/guide?key="+innerKey,
		map[string]string{
			"x-origin":      "https://www.youtube.com",
			"authorization": "SAPISIDHASH " + y.getSAPISIDHASH(SAPISID, "https://www.youtube.com"),
			"content-type":  "application/json",
		},
		[]byte(`{"context":{"client":{"clientName":"WEB","clientVersion":"2.20201220.08.00"},"user":{}},"fetchLiveState":true}`),
	)
	if err != nil {
		return err
	}
	baseUids, err := y.parseBaseStatus(string(rawPageBase))
	if err != nil {
		return err
	}
	for _, chanId := range baseUids {
		if _, ok := livingUids[chanId]; !ok {
			liveinfo, err := getVideoInfo(ctx, RandChooseStr(apihosts), chanId)
			if liveinfo != nil {
				livingUids[chanId] = *liveinfo
			} else {
				log.WithError(err).Warnf("Failed to get live info for channel %s", chanId)
			}
		}
	}

	y.LivingUids = livingUids
	return nil
}

func (y *YoutubePoller) GetStatus() error {
	return y.getLiveStatus()
}

func (y *YoutubePoller) StartPoll() error {
	err := y.GetStatus()
	if err != nil {
		return err
	}
	mod := base.GetMod("Youtube")
	_interval, ok := mod.ExtraConfig["PollInterval"]
	interval := time.Duration(config.Config.CriticalCheckSec) * time.Second
	if ok {
		interval = time.Duration(_interval.(float64)) * time.Second
	}
	go func() {
		for {
			time.Sleep(interval)
			err := y.GetStatus()
			if err != nil {
				log.WithError(err).Warnf("Error during polling GetStatus")
			}
		}
	}()
	return nil
}

func (y *YoutubePoller) IsLiving(uid string) *base.LiveInfo {
	y.lock.Lock()
	if y.LivingUids == nil {
		err := y.StartPoll()
		if err != nil {
			log.WithError(err).Warnf("Failed to poll from youtube")
		}
	}
	y.lock.Unlock()
	info, ok := y.LivingUids[uid]
	if ok {
		return &info
	} else {
		return nil
	}
}

func (b *Youtube) getVideoInfoByPoll() error {
	ret := U2bPoller.IsLiving(b.usersConfig.TargetId)
	b.IsLive = ret != nil
	if !b.IsLive {
		return nil
	}

	b.Target = ret.StreamingLink
	b.Title = ret.Title
	return nil
}

func (y *Youtube) CreateVideo(usersConfig config.UsersConfig) *interfaces.VideoInfo {
	if !y.yfConfig.IsLive {
		return &interfaces.VideoInfo{}
	}
	v := &interfaces.VideoInfo{
		Title:       y.Title,
		Date:        GetTimeNow(),
		Target:      y.Target,
		Provider:    "Youtube",
		UsersConfig: usersConfig,
	}
	return v
}
func (y *Youtube) CheckLive(usersConfig config.UsersConfig) bool {
	y.usersConfig = usersConfig
	err := y.getVideoInfoByPoll()
	if err != nil {
		y.IsLive = false
	}
	if !y.IsLive {
		base.NoLiving("Youtube", usersConfig.Name)
	}
	return y.yfConfig.IsLive
}

//func (y *Youtube) StartMonitor(usersConfig UsersConfig) {
//	if y.CheckLive(usersConfig) {
//		ProcessVideo(y.createVideo(usersConfig))
//	}
//}
