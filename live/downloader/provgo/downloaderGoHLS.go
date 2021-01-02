package provgo

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	m3u8Parser "github.com/etherlabsio/go-m3u8/m3u8"
	"github.com/fzxiao233/Vtb_Record/live/downloader/stealth"
	"github.com/fzxiao233/Vtb_Record/live/interfaces"
	"github.com/fzxiao233/Vtb_Record/utils"
	lru "github.com/hashicorp/golang-lru"
	"github.com/patrickmn/go-cache"
	log "github.com/sirupsen/logrus"
	"github.com/valyala/bytebufferpool"
	"golang.org/x/sync/semaphore"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

type HLSSegment struct {
	SegNo         int
	SegArriveTime time.Time
	Url           string
	//Data          []byte
	Data *bytes.Buffer
}

type HLSDownloader struct {
	Logger             *log.Entry
	M3U8UrlRewriter    stealth.URLRewriter
	GeneralUrlRewriter stealth.URLRewriter
	AltAsMain          bool
	OutPath            string
	Video              *interfaces.VideoInfo
	Cookie             string

	HLSUrl         string
	HLSHeader      map[string]string
	AltHLSUrl      string
	AltHLSHeader   map[string]string
	UrlUpdating    sync.Mutex
	AltUrlUpdating sync.Mutex
	altFallback    bool

	Clients    []*http.Client
	AltClients []*http.Client
	allClients []*http.Client
	clientsMap map[*http.Client]string

	SeqMap     sync.Map
	AltSeqMap  *lru.Cache
	SegLen     float64
	FinishSeq  int
	lastSeqNo  int
	Stopped    bool
	AltStopped bool
	output     io.Writer
	//segRl      ratelimit.Limiter
	segRl *semaphore.Weighted

	firstSeqChan chan int
	hasAlt       bool

	errChan    chan error
	alterrChan chan error

	forceRefreshChan    chan int
	altforceRefreshChan chan int

	downloadErr    *cache.Cache
	altdownloadErr *cache.Cache

	altSegErr sync.Map
}

var bufPool bytebufferpool.Pool

var IsStub = false

func (d *HLSDownloader) peekSegment(con context.Context, segData *HLSSegment) bool {
	logger := d.Logger.WithField("alt", false)

	// prepare the client
	onlyAlt := false
	// gotcha104 is tencent yun, only m3u8 blocked the foreign ip, so after that we simply ignore it
	/*if strings.Contains(segData.Url, "gotcha104") {
		onlyAlt = true
	}*/
	clients := d.allClients
	if onlyAlt {
		clients = d.AltClients
		if len(clients) == 0 {
			clients = d.allClients
		}
	} else {
		//segData.Url =  d.GeneralUrlRewriter.Rewrite(segData.Url)
		useMain, useAlt := stealth.GetAltProxyRuleForUrl(segData.Url)
		clients = d.GetClients(useMain, useAlt, true)
		//logger.Tracef("Downloading segment %d (%s) useMain %d useAlt %d", segData.SegNo, segData.Url, useMain, useAlt)
	}

	round := 0
	for {
		round += 1
		var res *http.Response
		//con, _ := context.WithTimeout(context.Background(), time.Second * 90)
		newHdr := make(map[string]string)
		for k, v := range d.HLSHeader {
			newHdr[k] = v
		}
		newHdr["Range"] = fmt.Sprintf("bytes=%d-", 2147483645)

		res, _ = utils.HttpDo(con, clients[0], "GET", segData.Url, newHdr, nil)
		if res == nil {
			time.Sleep(time.Second * 4)
			continue
		}
		tempbuf := make([]byte, 4)
		_, _ = res.Body.Read(tempbuf)
		if res.StatusCode == 416 {
			//logger.Infof("Successfully peeked: %s", segData.Url)
			return true
		}
		if res.StatusCode != 200 && res.StatusCode != 206 {
			logger.Warnf("Failed to peek due to unknown status code %d: %s", res.StatusCode, segData.Url)
			return false
		}
		time.Sleep(time.Second * 5)
		if round > 30 {
			logger.Warnf("Failed to peek within timeout: %s", segData.Url)
			return false
		}
	}
}

func (d *HLSDownloader) downloadSegment(segData *HLSSegment) bool {
	logger := d.Logger.WithField("alt", false)

	if err := d.segRl.Acquire(context.Background(), 1); err == nil {
		defer d.segRl.Release(1)
	}

	// download using a client
	downChan := make(chan *bytes.Buffer)
	defer func() {
		defer func() {
			recover()
		}()
		close(downChan)
	}()
	doDownload := func(con context.Context, client *http.Client) bool {
		s := time.Now()
		clientType, ok := d.clientsMap[client]
		if !ok {
			clientType = "unknown"
		}
		var newbuf *bytes.Buffer
		var err error
		//newbuf, err = utils.HttpGetBuffer(client, segData.Url, d.HLSHeader, nil)
		var retBuf []byte
		retBuf, err = utils.HttpMultiDownload(con, client, "GET", segData.Url, d.HLSHeader, nil, 400*1024*2)
		if retBuf != nil {
			newbuf = bytes.NewBuffer(retBuf)
		}
		if err != nil {
			logger.WithError(err).Infof("Err when download segment %s with %s client", segData.Url, clientType)
			// if it's 404, then we'll never be able to download it later, stop the useless retry
			if strings.HasSuffix(err.Error(), "404") {
				func() {
					defer func() {
						recover()
					}()
					ch := downChan
					if ch == nil {
						return
					}
					ch <- nil
				}()
			}
		} else {
			usedTime := time.Now().Sub(s)
			if usedTime > time.Second*20 {
				// we used too much time to download a segment
				logger.Infof("Download %d used %s with %s client (%s)", segData.SegNo, usedTime, clientType, segData.Url)
			}
			func() {
				defer func() {
					recover()
				}()
				ch := downChan
				if ch == nil {
					return
				}
				ch <- newbuf
			}()
		}
		return true
	}

	// prepare the client
	onlyAlt := false
	// gotcha104 is tencent yun, only m3u8 blocked the foreign ip, so after that we simply ignore it
	/*if strings.Contains(segData.Url, "gotcha104") {
		onlyAlt = true
	}*/
	clients := d.allClients
	if onlyAlt {
		clients = d.AltClients
		if len(clients) == 0 {
			clients = d.allClients
		}
	} else {
		//segData.Url =  d.GeneralUrlRewriter.Rewrite(segData.Url)
		useMain, useAlt := stealth.GetAltProxyRuleForUrl(segData.Url)
		clients = d.GetClients(useMain, useAlt, true)
		//logger.Tracef("Downloading segment %d (%s) useMain %d useAlt %d", segData.SegNo, segData.Url, useMain, useAlt)
	}

	i := 0
	// we one by one use each clients to download the segment, the first returned downloader wins
	// normally each hls seg will exist for 1 minutes
	round := 0
	curCon, curConCancel := context.WithCancel(context.Background())
	defer curConCancel()
breakout:
	for {
		i %= len(clients)
		go doDownload(curCon, clients[i])
		i += 1
		select {
		case ret := <-downChan:
			close(downChan)
			if ret == nil { // unrecoverable error, so reture at once
				return false
			}
			segData.Data = ret
			break breakout
		case <-time.After(80 * time.Second):
			// wait 10 second for each download try
		}
		if i == len(clients) {
			logger.Warnf("Failed all-clients to download segment %d (%s)", segData.SegNo, segData.Url)
			round++
		}
		if time.Now().Sub(segData.SegArriveTime) > 120*time.Second {
			logger.Warnf("Failed to download segment %d within timeout...", segData.SegNo)
			return false
		}
	}
	if round > 0 {
		// log the too long seg download and alt seg download
		logger.Infof("Downloaded segment %d (%s): len %v client %d", segData.SegNo, segData.Url, segData.Data.Len(), i)
	} else {
		logger.Tracef("Downloaded segment %d (%s): len %v client %d", segData.SegNo, segData.Url, segData.Data.Len(), i)
	}
	return true
}

// peek each segment first
func (d *HLSDownloader) handleSegment(segData *HLSSegment) bool {
	// rate limit the download speed...
	//d.segRl.Take()
	if IsStub {
		return true
	}

	//logger := d.Logger.WithField("alt", false)

	if strings.Contains(segData.Url, "gotcha105") {
		PEEK_TIME := time.Second * 110
		PEEK_NUMBER := 1
		con, concancel := context.WithTimeout(context.Background(), PEEK_TIME)
		//peekChan := make(chan int)
		for i := 0; i < PEEK_NUMBER; i++ {
			// peek it first!
			/*func() {
				if doDownload(con, clients[0], true) {
					peekChan <- 1
				}
			}()*/
			if d.peekSegment(con, segData) {
				concancel()
				break
			}
			time.Sleep(PEEK_TIME / time.Duration(PEEK_NUMBER))
		}
		<-con.Done()
		//time.Sleep(PEEK_TIME / time.Duration(PEEK_NUMBER))
		/*
			select {
			case <-con.Done():
				;
			case <-peekChan:
				concancel()
			}*/
	}
	return d.downloadSegment(segData)
}

type ParserStatus int32

const (
	Parser_OK       ParserStatus = 0
	Parser_FAIL     ParserStatus = 1
	Parser_REDIRECT ParserStatus = 2
)

// parse the m3u8 file to get segment number and url
func (d *HLSDownloader) m3u8Parser(parsedurl *url.URL, m3u8 string, isAlt bool) (status ParserStatus, additionalData interface{}) {
	logger := d.Logger.WithField("alt", isAlt)
	relaUrl := "http" + "://" + parsedurl.Host + path.Dir(parsedurl.Path)
	hostUrl := "http" + "://" + parsedurl.Host
	// if url is /XXX.ts, then it's related to host, if the url is XXX.ts, then it's related to url path
	getSegUrl := func(url string) string {
		if strings.HasPrefix(url, "http") {
			return url
		} else if url[0:1] == "/" {
			return hostUrl + url
		} else {
			return relaUrl + "/" + url
		}
	}

	playlist, err := m3u8Parser.ReadString(m3u8)
	if err != nil {
		return Parser_FAIL, err
	}

	curseq := playlist.Sequence

	if curseq == -1 {
		// curseq parse failed
		logger.Warnf("curseq parse failed!!!")
		return Parser_FAIL, nil
	}

	segs := make([]string, 0)

	seg_i := 0
	for _, _item := range playlist.Items {
		switch item := _item.(type) {
		case *m3u8Parser.PlaylistItem:
			//log.Debugf("Got redirect m3u8, redirecting to %s", item.URI)
			return Parser_REDIRECT, item.URI
		case *m3u8Parser.SegmentItem:
			seqNo := curseq + seg_i
			if playlist.IsLive() && seg_i == 0 {
				d.SegLen = item.Duration
			}
			seg_i += 1
			segs = append(segs, item.Segment)

			if !isAlt {
				_segData, loaded := d.SeqMap.LoadOrStore(seqNo, &HLSSegment{SegNo: seqNo, SegArriveTime: time.Now(), Url: getSegUrl(item.Segment)})
				if !loaded {
					segData := _segData.(*HLSSegment)
					logger.Debugf("Got new seg %d %s", seqNo, segData.Url)
					go d.handleSegment(segData)
				}
			} else {
				d.AltSeqMap.PeekOrAdd(seqNo, &HLSSegment{SegNo: seqNo, SegArriveTime: time.Now(), Url: getSegUrl(item.Segment)})
			}
		}
	}
	if !isAlt && d.firstSeqChan != nil {
		d.firstSeqChan <- curseq
		d.firstSeqChan = nil
	}
	if !isAlt {
		d.lastSeqNo = curseq + len(segs)
	}
	if !playlist.IsLive() {
		d.FinishSeq = curseq + len(segs) - 1
	}

	return Parser_OK, nil
}

func (d *HLSDownloader) forceRefresh(isAlt bool) {
	defer func() {
		recover()
	}()
	ch := d.forceRefreshChan
	if !isAlt {
		ch = d.forceRefreshChan
	} else {
		ch = d.altforceRefreshChan
	}
	if ch == nil {
		return
	}
	ch <- 1
}

func (d *HLSDownloader) sendErr(err error) {
	defer func() {
		recover()
	}()
	ch := d.errChan
	if ch == nil {
		return
	}
	ch <- err
}

func (d *HLSDownloader) getHLSUrl(isAlt bool) (curUrl string, curHeader map[string]string) {
	if !isAlt {
		d.UrlUpdating.Lock()
		curUrl = d.HLSUrl
		curHeader = d.HLSHeader
		d.UrlUpdating.Unlock()
	} else {
		d.AltUrlUpdating.Lock()
		curUrl = d.AltHLSUrl
		curHeader = d.AltHLSHeader
		d.AltUrlUpdating.Unlock()
	}
	return
}

func (d *HLSDownloader) setHLSUrl(isAlt bool, curUrl string, curHeader map[string]string) {
	if !isAlt {
		d.UrlUpdating.Lock()
		d.HLSUrl = curUrl
		if curHeader != nil {
			d.HLSHeader = curHeader
		}
		d.UrlUpdating.Unlock()
	} else {
		d.AltUrlUpdating.Lock()
		d.AltHLSUrl = curUrl
		if curHeader != nil {
			d.AltHLSHeader = curHeader
		}
		d.AltUrlUpdating.Unlock()
	}
	return
}

func (d *HLSDownloader) GetClients(useMain, useAlt int, isSegUrl bool) []*http.Client {
	clients := []*http.Client{}
	if useMain == 0 {
		clients = append(clients, d.AltClients...)
	} else if useAlt == 0 {
		clients = append(clients, d.Clients...)
		clients = append(clients, d.Clients...)
		if !isSegUrl {
			clients = append(clients, d.AltClients...)
		}
	} else {
		if useAlt > useMain {
			clients = append(clients, d.AltClients...)
			clients = append(clients, d.Clients...)
		} else {
			clients = d.allClients
		}
	}
	if len(clients) == 0 {
		clients = d.allClients
	}
	return clients
}

type M3u8ParserCallback interface {
	m3u8Parser(parsedurl *url.URL, m3u8 string, isAlt bool) (status ParserStatus, additionalData interface{})
}

// the core worker that download the m3u8 file
func (d *HLSDownloader) m3u8Handler(isAlt bool, parser M3u8ParserCallback) error {
	var err error
	logger := d.Logger.WithField("alt", isAlt)

	// if too many errors occurred during the m3u8 downloading, then we refresh the url
	errCache := d.downloadErr
	if isAlt {
		errCache = d.altdownloadErr
	}
	errCache.DeleteExpired()
	if errCache.ItemCount() >= 5 {
		errs := make([]interface{}, 0, 10)
		for _, e := range errCache.Items() {
			errs = append(errs, e)
		}
		errCache.Flush()
		url, _ := d.getHLSUrl(isAlt)
		logger.WithField("errors", errs).Warnf("Too many err occured downloading %s, refreshing m3u8url...", url)
		d.forceRefresh(isAlt)
		//time.Sleep(5 * time.Second)
	}

	// setup the worker chan
	retchan := make(chan []byte, 1)
	defer func() {
		defer func() {
			recover()
		}()
		close(retchan)
	}()

	if retchan == nil {
		retchan = make(chan []byte, 1)
	}

	// prepare the url
	var curUrl string
	var curHeader map[string]string
	curUrl, curHeader = d.getHLSUrl(isAlt)
	if curUrl == "" {
		logger.Infof("got empty m3u8 url")
		d.forceRefresh(isAlt)
		time.Sleep(10 * time.Second)
		return nil
	}
	_, err = url.Parse(curUrl)
	if err != nil {
		logger.WithError(err).Warnf("m3u8 url parse fail")
		d.forceRefresh(isAlt)
		//time.Sleep(10 * time.Second)
		return nil
	}
	curUrl = d.M3U8UrlRewriter.Rewrite(curUrl) // do some transform to avoid the rate limit from provider

	// request the m3u8
	doQuery := func(client *http.Client) {
		m3u8CurUrl := curUrl
		for {
			if _, ok := curHeader["Accept-Encoding"]; ok { // if there's custom Accept-Encoding, http.Client won't process them for us
				delete(curHeader, "Accept-Encoding")
			}
			_m3u8, err := utils.HttpGet(client, m3u8CurUrl, curHeader)
			if err != nil {
				d.M3U8UrlRewriter.Callback(m3u8CurUrl, err)
				logger.WithError(err).Debugf("Download m3u8 failed")
				// if it's client error like 404/403, then we need to abort
				if strings.HasSuffix(err.Error(), "404") || strings.HasSuffix(err.Error(), "403") {
					func() {
						defer func() {
							recover()
						}()
						logger.WithError(err).Warnf("Download aborting because of 404/403")
						ch := retchan
						if ch == nil {
							return
						}
						ch <- nil // abort!
					}()
				} else {
					if !isAlt {
						d.downloadErr.SetDefault(strconv.Itoa(int(time.Now().Unix())), err)
					} else {
						d.altdownloadErr.SetDefault(strconv.Itoa(int(time.Now().Unix())), err)
					}
				}
			} else {
				func() {
					defer func() {
						recover()
					}()
					ch := retchan
					if ch == nil {
						return
					}
					ch <- _m3u8 // abort!
				}()
				//logger.Debugf("Downloaded m3u8 in %s", time.Now().Sub(start))
				m3u8 := string(_m3u8)
				m3u8parsedurl, _ := url.Parse(m3u8CurUrl)
				//ret, info := d.m3u8Parser(m3u8parsedurl, m3u8, isAlt)
				ret, info := parser.m3u8Parser(m3u8parsedurl, m3u8, isAlt)
				if ret == Parser_REDIRECT {
					newUrl := info.(string)
					logger.Infof("Got redirect to %s!", newUrl)
					d.setHLSUrl(isAlt, newUrl, curHeader)
					//m3u8CurUrl = newUrl
					//continue
				} else if ret == Parser_OK {
					// perfect!
				} else {
					// oh no
					logger.Warnf("Failed to parse m3u8: %s", m3u8)
				}
			}
			return
		}
	}

	useMain, useAlt := stealth.GetAltProxyRuleForUrl(curUrl)
	clients := d.GetClients(useMain, useAlt, false)

	timeout := time.Millisecond * 1500
	if isAlt {
		timeout = time.Millisecond * 2500
	}
breakout:
	for i, client := range clients {
		go doQuery(client)
		select {
		case ret := <-retchan:
			close(retchan)
			retchan = nil
			if ret == nil {
				//logger.Info("Unrecoverable m3u8 download err, aborting")
				return fmt.Errorf("Unrecoverable m3u8 download err, aborting, url: %s", curUrl)
			}
			if !isAlt {
				d.downloadErr.Flush()
			} else {
				d.altdownloadErr.Flush()
			}
			break breakout
		case <-time.After(timeout): // failed to download within timeout, issue another req
			logger.Debugf("Download m3u8 %s timeout with client %d", curUrl, i)
		}
	}
	return nil
}

// query main m3u8 every 2 seconds
func (d *HLSDownloader) Downloader() {
	curDuration := 2.0
	ticker := time.NewTicker(time.Duration(float64(time.Second) * curDuration))
	breakflag := false
	for {
		go func() {
			err := d.m3u8Handler(false, d)
			if err != nil {
				d.sendErr(err) // we have error, break out now
				breakflag = true
				return
			}
		}()
		if breakflag {
			return
		}
		if d.FinishSeq > 0 {
			d.Stopped = true
		}
		if d.Stopped {
			break
		}
		<-ticker.C // if the handler takes too long, the next tick will arrive at once
		if d.SegLen < curDuration {
			ticker.Stop()
			curDuration = d.SegLen * 0.8
			if curDuration < 0.8 {
				curDuration = 0.8
			}
			d.Logger.Infof("Using new hls interval: %f", curDuration)
			ticker = time.NewTicker(time.Duration(float64(time.Second) * curDuration))
		}
	}
	ticker.Stop()
}

// update the main hls stream's link
func (d *HLSDownloader) Worker() {
	ticker := time.NewTicker(time.Minute * 40)
	defer ticker.Stop()
	for {
		if d.forceRefreshChan == nil {
			d.forceRefreshChan = make(chan int)
		}
		if d.Stopped {
			<-ticker.C // avoid busy loop
		} else {
			select {
			case _ = <-ticker.C:

			case _ = <-d.forceRefreshChan:
				d.Logger.Info("Got forceRefresh signal, refresh at once!")
				isClose := false
				func() {
					defer func() {
						panicMsg := recover()
						if panicMsg != nil {
							isClose = true
						}
					}()
					close(d.forceRefreshChan)
					d.forceRefreshChan = nil // avoid multiple refresh
				}()
				if isClose {
					return
				}
			}
		}
		retry := 0
		for {
			// try at most 20 times
			retry += 1
			if retry > 1 {
				time.Sleep(30 * time.Second)
				if retry > 20 {
					d.sendErr(fmt.Errorf("failed to update playlist in 20 attempts"))
					return
				}
				if d.Stopped {
					return
				}
			}
			alt := d.AltAsMain

			// check if we have error or need abort
			needAbort, err, infoJson := updateInfo(d.Video, "", d.Cookie, alt)
			if needAbort {
				d.Logger.WithError(err).Warnf("Streamlink requests to abort, worker finishing...")
				// if we have entered live
				d.sendErr(fmt.Errorf("Streamlink requests to abort: %s", err))
				return
			}
			if err != nil {
				d.Logger.WithError(err).Warnf("Failed to update playlist")
				continue
			}
			m3u8url, headers, err := parseHttpJson(infoJson)
			if err != nil {
				d.Logger.WithError(err).Warnf("Failed to parse json ret")
				continue
			}

			// update hls url
			d.Logger.Infof("Got new m3u8url: %s", m3u8url)
			if m3u8url == "" {
				d.Logger.Warnf("Got empty m3u8 url...: %s", infoJson)
				continue
			}
			d.UrlUpdating.Lock()
			d.HLSUrl = m3u8url
			d.HLSHeader = headers
			d.UrlUpdating.Unlock()
			break
		}
		if d.Stopped {
			return
		}
	}
}

// test stub for writer
func (d *HLSDownloader) WriterStub() {
	for {
		timer := time.NewTimer(time.Second * time.Duration((50+rand.Intn(20))/10))
		d.output.Write(randData)
		<-timer.C
	}
}

// Responsible to write out each segments
func (d *HLSDownloader) Writer() {
	getMinNo := func() int {
		minNo := -1
		d.SeqMap.Range(func(key, value interface{}) bool {
			cur := key.(int)
			if minNo == -1 {
				minNo = cur
			}
			if key.(int) < minNo {
				minNo = cur
			}
			return true
		})
		return minNo
	}

	// get the seq of first segment, then start the writing
	curSeq := <-d.firstSeqChan
	for {
		// calculate the load time, so that we can check the timeout
		loadTime := time.Second * 0
		//d.Logger.Debugf("Loading segment %d", curSeq)
		for {
			_val, ok := d.SeqMap.Load(curSeq)
			if ok {
				// the segment has already been retrieved
				val := _val.(*HLSSegment)
				if curSeq >= 30 {
					d.SeqMap.Delete(curSeq - 30)
				}

				if val.Data != nil {
					// segment has been downloaded
					timeoutChan := make(chan int, 1)
					go func(timeoutChan chan int, startTime time.Time, segNo int) {
						// detect writing timeout
						timer := time.NewTimer(15 * time.Second)
						select {
						case <-timeoutChan:
							d.Logger.Debugf("Wrote segment %d in %s", segNo, time.Now().Sub(startTime))
						case <-timer.C:
							d.Logger.Warnf("Write segment %d too slow...", curSeq)
							timer2 := time.NewTimer(60 * time.Second)
							select {
							case <-timeoutChan:
								d.Logger.Debugf("Wrote segment %d in %s", segNo, time.Now().Sub(startTime))
							case <-timer2.C:
								d.Logger.Errorf("Write segment %d timeout!!!!!!!", curSeq)
							}
						}
					}(timeoutChan, time.Now(), curSeq)
					_, err := d.output.Write(val.Data.Bytes())
					timeoutChan <- 1

					//bufPool.Put(val.Data)
					val.Data = nil
					if err != nil {
						d.sendErr(err)
						return
					}
					break
				}
				// segment still not downloaded, increase the load time
			} else {
				if loadTime > 20*time.Second {
					// segment is not loaded
					if d.lastSeqNo > 3 && d.lastSeqNo+2 < curSeq { // seqNo got reset to 0
						d.Logger.Warnf("Failed to load segment %d due to segNo got reset to %d", curSeq)
						// exit ASAP so that alt stream will be preserved
						curSeq = getMinNo()
						continue
						//d.sendErr(fmt.Errorf("Failed to load segment %d due to segNo got reset to %d", curSeq, d.lastSeqNo))
						//return
					} else {
						// detect if we are lagged (e.g. we are currently at seg2, still waiting for seg3 to appear, however seg4 5 6 7 has already been downloaded)
						isLagged := false
						d.SeqMap.Range(func(key, value interface{}) bool {
							if key.(int) > curSeq+3 && value.(*HLSSegment).Data != nil {
								d.Logger.Warnf("curSeq %d lags behind segData %d!", curSeq, key.(int))
								isLagged = true
								return false
							} else {
								return true
							}
						})
						if isLagged { // exit ASAP so that alt stream will be preserved
							curSeq += 1
							//d.sendErr(fmt.Errorf("Failed to load segment %d within m3u8 timeout due to lag...", curSeq))
							d.Logger.Warnf("Failed to load segment %d within m3u8 timeout due to lag, continuing to next seg", curSeq)
							continue
						}
					}
				}
			}
			time.Sleep(500 * time.Millisecond)
			loadTime += 500 * time.Millisecond
			// if load time is too long, then likely the recording is interrupted
			if loadTime == 1*time.Minute || loadTime == 150*time.Second || loadTime == 240*time.Second {
				go d.AltSegDownloader() // trigger alt download in advance, so we can avoid more loss
			}
			if loadTime > 5*time.Minute { // segNo shouldn't return to 0 within 5 min
				// if we come to here, then the lag detect would already failed, the live must be interrupted
				d.sendErr(fmt.Errorf("Failed to load segment %d within timeout...", curSeq))
				return
			}
			if curSeq == d.FinishSeq { // successfully finished
				d.sendErr(nil)
				return
			}
		}
		curSeq += 1
	}
}

func (d *HLSDownloader) startDownload() error {
	var err error

	d.FinishSeq = -1
	// rate limit, so we won't break up all things
	//d.segRl = ratelimit.New(1)

	//d.segRl = rate.NewLimiter(rate.Every(time.Second*5), 1)
	d.segRl = semaphore.NewWeighted(3)

	d.SegLen = 2.0

	writer := utils.GetWriter(d.OutPath)
	d.output = writer
	defer writer.Close()

	d.allClients = make([]*http.Client, 0)
	d.allClients = append(d.allClients, d.Clients...)
	d.allClients = append(d.allClients, d.AltClients...)

	d.clientsMap = make(map[*http.Client]string)
	for _, client := range d.Clients {
		d.clientsMap[client] = "main"
	}
	for _, client := range d.AltClients {
		d.clientsMap[client] = "alt"
	}

	d.AltSeqMap, _ = lru.New(16)
	d.errChan = make(chan error)
	d.alterrChan = make(chan error)
	d.firstSeqChan = make(chan int)
	d.forceRefreshChan = make(chan int)
	d.altforceRefreshChan = make(chan int)
	d.downloadErr = cache.New(30*time.Second, 5*time.Minute)
	d.altdownloadErr = cache.New(30*time.Second, 5*time.Minute)

	d.hasAlt = false
	if _, ok := d.Video.UsersConfig.ExtraConfig["AltStreamLinkArgs"]; ok {
		d.hasAlt = true
	}

	if !d.hasAlt && d.AltAsMain {
		return fmt.Errorf("Current live does not have alt source")
	}

	if IsStub {
		d.hasAlt = false
		go d.WriterStub()
	} else {
		go d.Writer()
	}

	go d.Downloader()
	go d.Worker()

	if !d.AltAsMain && d.hasAlt {
		d.Logger.Debugf("Use alt downloader")

		// start the alt downloader 60 seconds later to avoid the burst query of streamlink
		time.AfterFunc(60*time.Second, func() {
			go func() {
				for {
					d.AltWorker()
					if d.AltStopped {
						break
					}
				}
			}()
			d.altforceRefreshChan <- 1
			// start the downloader later so that the url is already initialized
			time.AfterFunc(30*time.Second, d.AltDownloader)
		})
	} else {
		d.Logger.Infof("Disabled alt downloader")
	}

	startTime := time.Now()
	err = <-d.errChan
	usedTime := time.Now().Sub(startTime)
	if err == nil {
		d.Logger.Infof("HLS Download successfully!")
		d.AltStopped = true
	} else {
		d.Logger.Infof("HLS Download failed: %s", err)
		if d.hasAlt {
			if usedTime > 1*time.Minute {
				go d.AltWriter()
			} else {
				d.AltStopped = true
			}
		}
	}
	func() {
		defer func() {
			recover()
		}()
		close(d.errChan)
		close(d.forceRefreshChan)
	}()
	d.Stopped = true
	d.SeqMap = sync.Map{}
	defer func() {
		go func() {
			time.Sleep(3 * time.Minute)
			d.AltStopped = true
		}()
	}()
	return err
}

// initialize the go hls downloader
func (dd *DownloaderGo) doDownloadHls(entry *log.Entry, output string, video *interfaces.VideoInfo, m3u8url string, headers map[string]string, needMove bool) error {
	clients := []*http.Client{
		{
			Transport: &http.Transport{
				ResponseHeaderTimeout: 20 * time.Second,
				TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
				DisableCompression:    true,
				DisableKeepAlives:     false,
				TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
				DialContext:           http.DefaultTransport.(*http.Transport).DialContext,
				DialTLS:               http.DefaultTransport.(*http.Transport).DialTLS,
			},
			Timeout: 180 * time.Second,
		},
	}

	_altproxy, ok := video.UsersConfig.ExtraConfig["AltProxy"]
	var altproxy string
	var altclients []*http.Client
	if ok {
		altproxy = _altproxy.(string)
		proxyUrl, _ := url.Parse("socks5://" + altproxy)
		altclients = []*http.Client{
			{
				Transport: &http.Transport{
					TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
					Proxy:        http.ProxyURL(proxyUrl),
					//DisableCompression: true,
					DisableKeepAlives: false,
					TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
				},
				Timeout: 100 * time.Second,
			},
		}
	} else {
		altclients = []*http.Client{}
	}

	d := &HLSDownloader{
		Logger:             entry,
		AltAsMain:          dd.useAlt,
		HLSUrl:             m3u8url,
		HLSHeader:          headers,
		AltHLSUrl:          m3u8url,
		AltHLSHeader:       headers,
		Clients:            clients,
		AltClients:         altclients,
		Video:              video,
		OutPath:            output,
		Cookie:             dd.cookie,
		M3U8UrlRewriter:    stealth.GetRewriter(),
		GeneralUrlRewriter: stealth.GetRewriter(),
		//output:    out,
	}

	err := d.startDownload()
	time.Sleep(1 * time.Second)
	utils.ExecShell("/home/misty/rclone", "rc", "vfs/forget", "dir="+path.Dir(output))
	return err
}
