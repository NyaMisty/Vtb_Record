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
		//[]byte(`{"context":{"client":{"hl":"zh-CN","gl":"HK","geo":"HK","remoteHost":"150.109.53.169","isInternal":true,"deviceMake":"","deviceModel":"","visitorData":"CgtOWXh4TmlEcllGOCizqbz_BQ%3D%3D","userAgent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/87.0.4280.88 Safari/537.36,gzip(gfe)","clientName":"WEB","clientVersion":"2.20201220.08.00","osName":"Windows","osVersion":"10.0","originalUrl":"https://www.youtube.com/feed/subscriptions","internalClientExperimentIds":[3300013,3300101,3300130,3300161,3313321,3318024,3318700,3318774,3318776,3319220,3319460,3319711,3320540,3329006,3329299,3329602,3329621,44496011],"screenPixelDensity":2,"platform":"DESKTOP","gfeFrontlineInfo":"vip=172.217.160.78,server_port=443,client_port=10635,tcp_connection_request_count=0,header_order=HUAELC,gfe_version=2.699.6,ssl,ssl_info=TLSv1.3:RA:F,tlsext=S,sni=www.youtube.com,hex_encoded_client_hello=5a5a130113021303c02bc02fc02cc030cca9cca8c013c014009c009d002f0035-00-3a3a00000017ff01000a000b002300100005000d00120033002d002b001b2a2a0029,c=1301,pn=alpn,ja3=44d502d471cfdb99c59bdfb0f220e5a8,rtt_source=h2_ping,rtt=16,srtt=16,client_protocol=h2,client_transport=tcp,gfe=actsaf11.prod.google.com,pzf=Linux 2.2.x-3.x [4:51+13:0:1424:mss*5/7:mss/sok/ts/nop/ws:df/id+:0] [generic tos:0x20],vip_region=default,asn=132203,cc=HK,eid=sxTvX-fYI8uiX8TTjYgM,scheme=https","clientFormFactor":"UNKNOWN_FORM_FACTOR","screenDensityFloat":2,"countryLocationInfo":{"countryCode":"HK","countrySource":"COUNTRY_SOURCE_IPGEO_INDEX"},"browserName":"Chrome","browserVersion":"87.0.4280.88","screenWidthPoints":1920,"screenHeightPoints":881,"utcOffsetMinutes":480,"userInterfaceTheme":"USER_INTERFACE_THEME_LIGHT","connectionType":"CONN_CELLULAR_4G","mainAppWebInfo":{"graftUrl":"https://www.youtube.com/feed/subscriptions"},"timeZone":"Asia/Shanghai"},"user":{"gaiaId":269972135876,"userId":14345679337,"lockedSafetyMode":false},"request":{"useSsl":true,"sessionId":6909557054561602000,"parentEventId":{"timeUsec":1609503923588058,"serverIp":170556682,"processId":-452883592},"sessionIndex":3,"internalExperimentFlags":[],"consistencyTokenJars":[]},"clickTracking":{"clickTrackingParams":"IhMI2peFrd367QIVCn0qCh14iwHl"},"clientScreenNonce":"MC45MzU1MTM4Mjc2MTg5NzIx","adSignalsInfo":{"params":[{"key":"dt","value":"1609503923689"},{"key":"flash","value":"0"},{"key":"frm","value":"0"},{"key":"u_tz","value":"480"},{"key":"u_his","value":"9"},{"key":"u_java","value":"false"},{"key":"u_h","value":"1080"},{"key":"u_w","value":"1920"},{"key":"u_ah","value":"1040"},{"key":"u_aw","value":"1920"},{"key":"u_cd","value":"24"},{"key":"u_nplug","value":"3"},{"key":"u_nmime","value":"4"},{"key":"bc","value":"31"},{"key":"bih","value":"881"},{"key":"biw","value":"1900"},{"key":"brdim","value":"0,0,0,0,1920,0,1920,1040,1920,881"},{"key":"vis","value":"1"},{"key":"wgl","value":"true"},{"key":"ca_type","value":"image"}]}},"fetchLiveState":true}`),
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
