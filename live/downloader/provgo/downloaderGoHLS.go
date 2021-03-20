package provgo

import (
	"bytes"
	"container/ring"
	"context"
	"crypto/tls"
	"fmt"
	m3u8Parser "github.com/etherlabsio/go-m3u8/m3u8"
	"github.com/fzxiao233/Vtb_Record/config"
	"github.com/fzxiao233/Vtb_Record/live/downloader/stealth"
	"github.com/fzxiao233/Vtb_Record/live/interfaces"
	"github.com/fzxiao233/Vtb_Record/utils"
	lru "github.com/hashicorp/golang-lru"
	"github.com/patrickmn/go-cache"
	log "github.com/sirupsen/logrus"
	"go.uber.org/atomic"
	"go.uber.org/ratelimit"
	"golang.org/x/net/http2"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type HLSSegment struct {
	SegContext    context.Context
	SegConCancel  func()
	SegNo         int
	SegLen        float64
	SegArriveTime time.Time
	Url           string
	//Data          []byte
	Data *bytes.Buffer
}

type HLSDownloader struct {
	Context            context.Context
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
	h2Clients  []*http.Client
	segClients []*http.Client
	clientsMap map[*http.Client]string

	SeqMap     sync.Map
	AltSeqMap  *lru.Cache
	FinishSeq  int
	Stopped    bool
	AltStopped bool
	output     io.Writer

	lastSeqNo      int
	lastWriteSeqNo int
	writeLagNum    atomic.Int64
	segLens        *ring.Ring
	SegLen         float64

	rl          ratelimit.Limiter
	segRl       *utils.PrioritySemaphore
	downloading atomic.Int64

	firstSeqChan chan int
	hasAlt       bool

	errChan    chan error
	alterrChan chan error

	forceRefreshChan    chan int
	altforceRefreshChan chan int

	downloadErr    *cache.Cache
	altdownloadErr *cache.Cache

	altSegErr sync.Map

	loadBalanceData *LBData
	useH2           bool
}

var IsStub = false

/*func (d *HLSDownloader) peekSegmentWithWorker(con context.Context, segData *HLSSegment) bool {
	logger := d.Logger.WithField("alt", false)
	startTime := time.Now()
	useMain, useAlt := stealth.GetAltProxyRuleForUrl(segData.Url)
	clients := d.GetClients(useMain, useAlt, true)
	client := clients[0]
	tempCli := utils.CloneHttpClient(client, func(transport *http.Transport) http.RoundTripper {
		transport.DisableKeepAlives = true
		return transport
	})
}*/

func (d *HLSDownloader) peekSegmentStub(con context.Context, segData *HLSSegment) bool {
	return true
}

func (d *HLSDownloader) peekSegmentWithDownload(con context.Context, segData *HLSSegment) bool {
	logger := d.Logger.WithField("alt", false)
	startTime := time.Now()

	useMain, useAlt := stealth.GetAltProxyRuleForUrl(segData.Url)
	clients := d.GetClients(useMain, useAlt, true)
	client := clients[0]
	tempCli := utils.CloneHttpClient(client, func(transport *http.Transport) http.RoundTripper {
		transport.DisableKeepAlives = true
		transport.ResponseHeaderTimeout = 50 * time.Second
		return transport
	})

	var res *http.Response
	doReq := func() (*http.Response, error) {
		req, err := utils.HttpBuildRequest(con, "GET", segData.Url, d.HLSHeader, nil)
		if req == nil {
			return nil, err
		}
		//req.Close = true
		res, err = utils.HttpDoRequest(con, tempCli, req)
		if res == nil {
			return nil, err
		}
		tempbuf := make([]byte, 4)
		_, _ = res.Body.Read(tempbuf)
		_ = res.Body.Close()
		return res, nil
	}
	res, err := doReq()
	if res != nil {
		logger.Debugf("Successfully peeked segment %d in %v", segData.SegNo, time.Now().Sub(startTime))
		return true
	} else {
		logger.Debugf("Failed to peek segment %d in %v, rets: %v", segData.SegNo, time.Now().Sub(startTime), err)
		return false
	}
	//logger.Tracef("Downloading segment %d (%s) useMain %d useAlt %d", segData.SegNo, segData.Url, useMain, useAlt)
}

func (d *HLSDownloader) peekSegmentWithRange(con context.Context, segData *HLSSegment) bool {
	logger := d.Logger.WithField("alt", false)
	startTime := time.Now()
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
	client := clients[0]
	tempCli := utils.CloneHttpClient(client, func(transport *http.Transport) http.RoundTripper {
		transport.DisableKeepAlives = true
		return transport
	})

	round := 0
	rets := make([]string, 0)
	for {
		round += 1
		var res *http.Response
		//con, _ := context.WithTimeout(context.Background(), time.Second * 90)
		newHdr := make(map[string]string)
		for k, v := range d.HLSHeader {
			newHdr[k] = v
		}
		newHdr["Range"] = fmt.Sprintf("bytes=%d-", 2147483645)
		doReq := func() (*http.Response, error) {
			req, err := utils.HttpBuildRequest(con, "GET", segData.Url, newHdr, nil)
			if req == nil {
				return nil, err
			}
			//req.Close = true
			res, err = utils.HttpDoRequest(con, tempCli, req)
			if res == nil {
				return nil, err
			}
			tempbuf := make([]byte, 4)
			_, _ = res.Body.Read(tempbuf)
			_ = res.Body.Close()
			return res, nil
		}
		res, err := doReq()
		if res != nil {
			rets = append(rets, fmt.Sprintf("'status:%v'", res.StatusCode))
			if res.StatusCode == 416 {
				logger.Debugf("Successfully peeked segment %d in %v, rets: %v", segData.SegNo, time.Now().Sub(startTime), rets)
				//newHdr["Range"] = fmt.Sprintf("bytes=1-1,409601-409601,819201-819201,1228801-1882201,1638400-1638401," +
				//	"2048000-2048001,3072000-3072001,3584000-3584001,4096000-4096001")
				//doReq()
				return true
			}
			if res.StatusCode != 200 && res.StatusCode != 206 {
				logger.Warnf("Failed to peek segment %d due to unknown status code %d: %s", segData.SegNo, res.StatusCode, segData.Url)
				return false
			}
		} else {
			rets = append(rets, fmt.Sprintf("'err:%v'", err))
		}

		time.Sleep(time.Second * 3)
		if round > 8 {
			logger.Warnf("Failed to peek segment %d within timeout: %s, rets: %v", segData.SegNo, segData.Url, rets)
			return false
		}
	}
}

var SEG_DOWNLOAD_START_DDL = 80 * time.Second
var SEG_DOWNLOAD_TIMEOUT = 150 * time.Second

func (d *HLSDownloader) downloadSegment(segData *HLSSegment) bool {
	logger := d.Logger.WithField("alt", false)
	startTime := time.Now()

	var released atomic.Int64
	released.Inc()
	curPrio := segData.SegArriveTime.Unix()*1000000 + int64(segData.SegNo)%1000000
	for i := 0; i < 25 && time.Now().Sub(segData.SegArriveTime) < SEG_DOWNLOAD_START_DDL-5*time.Second; i++ {
		if segData.SegNo-d.lastWriteSeqNo > 5 {
			time.Sleep(1000 * time.Millisecond)
		} else {
			break
		}
	}
	semCon, _ := context.WithDeadline(segData.SegContext, segData.SegArriveTime.Add(SEG_DOWNLOAD_START_DDL))
	if err := d.segRl.Acquire(semCon, -curPrio); err == nil {
		if strings.Contains(d.HLSUrl, "gotcha105") {
			for i := 0; i < 12 && time.Now().Sub(segData.SegArriveTime) < SEG_DOWNLOAD_START_DDL; i++ {
				if segData.SegNo-d.lastWriteSeqNo > 5 {
					time.Sleep(1000 * time.Millisecond)
				} else {
					break
				}
			}
			go func() {
				if d.downloading.Load() >= 4 { // too many downloading
					return
				}
				if segData.SegNo-d.lastWriteSeqNo > 5 { // we are already over ahead, do not make it worse
					return
				}
				/*if d.lastSeqNo > segData.SegNo {
					laggedTime := time.Duration(segData.SegLen * float64(time.Second)) * time.Duration(d.lastSeqNo - segData.SegNo)
					deadline := 90 * time.Second - laggedTime
					if deadline > 0 {
						time.Sleep(deadline)
					}
				}*/
				time.Sleep(time.Duration(d.SegLen * float64(time.Second) * 2))
				relCount := released.Dec() + 1
				if relCount > 0 {
					d.segRl.Release()
				}
			}()
		}
		defer func() {
			relCount := released.Dec() + 1
			if relCount > 0 {
				d.segRl.Release()
			}
		}()
	}
	if err := segData.SegContext.Err(); err != nil {
		logger.Infof("Not downloading seg %d because segCon canceled, err: %v", segData.SegNo, err)
		return false
	}
	if d.Stopped {
		return false
	}
	d.downloading.Inc()
	defer d.downloading.Dec()

	beginTime := time.Now()
	finished := false
	// download using a client
	downChan := make(chan *bytes.Buffer)
	defer func() {
		defer func() {
			recover()
		}()
		close(downChan)
	}()
	var downloadCount atomic.Int32
	doDownload := func(con context.Context, client *http.Client) bool {
		downloadCount.Inc()
		defer func() {
			downloadCount.Dec()
		}()
		s := time.Now()
		clientType, ok := d.clientsMap[client]
		if !ok {
			clientType = "unknown"
		}
		var newbuf *bytes.Buffer
		var err error
		if strings.Contains(segData.Url, "gotcha105") {
			var retBuf []byte
			retBuf, err = utils.HttpMultiDownload(con, client, "GET", segData.Url, d.HLSHeader, nil, 400*1024*1)
			if retBuf != nil {
				newbuf = bytes.NewBuffer(retBuf)
			}
		} else {
			newbuf, err = utils.HttpGetBuffer(client, segData.Url, d.HLSHeader, nil)
		}
		if err != nil {
			if !finished {
				logger.WithError(err).Infof("Err when download segment %s with %s client", segData.Url, clientType)
			} else {
				logger.WithError(err).Debugf("Err when download segment %s with %s client", segData.Url, clientType)
			}
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
			dolog := logger.Debugf
			if usedTime > time.Second*39 {
				// we used too much time to download a segment
				dolog = logger.Infof
			}
			dolog("Download %d used %s with %s client (%s)", segData.SegNo, usedTime, clientType, segData.Url)
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
	curCon, curConCancel := context.WithCancel(segData.SegContext)
	curCon = context.WithValue(curCon, "logger", logger)
	defer curConCancel()

	var retryChan chan int
	defer func() {
		if retryChan != nil {
			ch := retryChan
			retryChan = nil
			close(ch)
		}
	}()
	var segBuffer *bytes.Buffer
breakout:
	for {
		i %= len(clients)
		go doDownload(curCon, clients[i])
		i += 1

		if retryChan != nil {
			ch := retryChan
			retryChan = nil
			close(ch)
		}
		retryChan = make(chan int)
		if strings.Contains(segData.Url, "gotcha105") {
			// do nothing and wait until last one finished
			go func(ch chan int) {
				defer func() {
					recover()
				}()
				tch := time.After(time.Second * 60)
				time.Sleep(6 * time.Second)

			breakout1:
				for {
					select {
					case <-tch:
						if downloadCount.Load() <= 1 {
							ch <- 1
							break breakout1
						}
					default:
					}
					if downloadCount.Load() > 0 {
						time.Sleep(2 * time.Second)
						continue
					}
					ch <- 1
					break
				}
			}(retryChan)
		} else {
			go func(ch chan int) {
				defer func() {
					recover()
				}()
				time.Sleep(6 * time.Second)
				<-time.After(45 * time.Second)
				ch <- 1
			}(retryChan)
		}

		select {
		case ret := <-downChan:
			close(downChan)
			if ret == nil { // unrecoverable error, so reture at once
				return false
			}
			segData.Data = ret
			segBuffer = ret
			break breakout
		case <-retryChan:
			// wait 10 second for each download try
		}
		if time.Now().Sub(segData.SegArriveTime) > SEG_DOWNLOAD_TIMEOUT {
			logger.Warnf("Failed to download segment %d within timeout...", segData.SegNo)
			return false
		}
		if curCon.Err() != nil {
			logger.Warnf("Cancelling download segment %d (%s) because %v", segData.SegNo, segData.Url, curCon.Err())
			return false
		}
		logger.Infof("Retrying seg %d download, last round %d client %d", segData.SegNo, round, i-1)
		if i == len(clients) {
			logger.Warnf("Failed all-clients to download segment %d (%s)", segData.SegNo, segData.Url)
			round++
		}
	}
	dolog := logger.Debugf
	if round > 0 {
		// log the too long seg download and alt seg download
		dolog = logger.Infof
	}
	dolog("Downloaded segment %d: len %v client %d using %v (wait %v)", segData.SegNo, segBuffer.Len(), i, time.Now().Sub(beginTime), beginTime.Sub(startTime))
	finished = true
	d.writeLagNum.Inc()
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

	PEEK_TIME := SEG_DOWNLOAD_START_DDL - 30*time.Second
	PEEK_NUMBER := 1
	con, concancel := context.WithTimeout(segData.SegContext, PEEK_TIME)
	for i := 0; i < PEEK_NUMBER; i++ {
		peekStart := time.Now()
		peeker := d.peekSegmentStub
		if strings.Contains(segData.Url, "gotcha105") {
			peeker = d.peekSegmentWithRange
		} else if strings.Contains(segData.Url, "gotcha104") {
			peeker = d.peekSegmentWithDownload
		}
		if peeker(con, segData) {
			concancel()
			break
		}
		time.Sleep(PEEK_TIME/time.Duration(PEEK_NUMBER) - time.Now().Sub(peekStart))
	}
	<-con.Done()
	time.Sleep(2 * time.Second)

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
			if playlist.IsLive() /* && seg_i == 0*/ {
				//d.SegLen = (d.SegLen * 3 + item.Duration) / 4
			}
			seg_i += 1
			segs = append(segs, item.Segment)

			con, concancel := context.WithCancel(d.Context)
			segItem := &HLSSegment{SegContext: con, SegConCancel: concancel, SegNo: seqNo, SegLen: item.Duration, SegArriveTime: time.Now(), Url: getSegUrl(item.Segment)}
			if !isAlt {
				_segData, loaded := d.SeqMap.LoadOrStore(seqNo, segItem)
				if !loaded {
					// update seglen
					d.segLens.Value = item.Duration
					d.segLens = d.segLens.Next()
					newLen := 0.0
					segnum := 0
					d.segLens.Do(func(i interface{}) {
						if t, ok := i.(float64); ok {
							newLen += t
							segnum += 1
						}
					})
					d.SegLen = newLen / float64(segnum)

					// handle segment
					segData := _segData.(*HLSSegment)
					if strings.Contains(segData.Url, "googlevideo") { // google's url is toooooo looooong
						logger.Debugf("Got new seg %d %s", seqNo, segData.Url[0:50])
					} else {
						logger.Debugf("Got new seg %d %s", seqNo, segData.Url)
					}
					go d.handleSegment(segData)
				}
			} else {
				d.AltSeqMap.PeekOrAdd(seqNo, segItem)
			}
		}
	}
	if !isAlt && d.firstSeqChan != nil {
		d.firstSeqChan <- curseq
		d.firstSeqChan = nil
	}
	if !isAlt {
		d.lastSeqNo = curseq + len(segs) - 1
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
			if strings.Contains(curUrl, "gotcha105") {
				d.HLSHeader["User-Agent"] = "nil"
			}
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
		if isSegUrl {
			clients = append(clients, d.segClients...)
			clients = append(clients, d.segClients...)
		} else {
			if d.useH2 {
				clients = append(clients, d.h2Clients...)
				clients = append(clients, d.h2Clients...)
			} else {
				clients = append(clients, d.Clients...)
				clients = append(clients, d.Clients...)
			}
			//clients = append(clients, d.AltClients...)
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
						ch := retchan
						if ch == nil {
							return
						}
						logger.WithError(err).Infof("Download aborting because of 404/403")
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

func (d *HLSDownloader) refreshClient() {
	for _, cli := range d.h2Clients {
		cli.CloseIdleConnections()
		oriTrans := cli.Transport.(*http2.Transport)
		oriTrans.CloseIdleConnections()
		cli.Transport = &http2.Transport{
			DialTLS:            oriTrans.DialTLS,
			TLSClientConfig:    oriTrans.TLSClientConfig,
			DisableCompression: oriTrans.DisableCompression,
			AllowHTTP:          oriTrans.AllowHTTP,
			ReadIdleTimeout:    oriTrans.ReadIdleTimeout,
			PingTimeout:        oriTrans.PingTimeout,
		}
	}
	for _, cli := range d.h2Clients {
		cli.CloseIdleConnections()
	}
	d.initLBData()
}

// query main m3u8 every 2 seconds
func (d *HLSDownloader) Downloader() {
	curDuration := 2.0
	ticker := time.NewTicker(time.Duration(float64(time.Second) * curDuration))
	breakflag := false
	lastHandleTime := time.Now()
	shakeCount := 0
	for {
		go func() {
			err := d.m3u8Handler(false, d)
			lastHandleTime = time.Now()
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
		lastUpdate := time.Now().Sub(lastHandleTime)
		if time.Now().Sub(lastHandleTime) > time.Duration(float64(time.Second)*1.2*d.SegLen) {
			d.Logger.Infof("Detected potential lag (%v since last update)! Retrying at once!", lastUpdate)
			lastHandleTime = time.Now()
			d.refreshClient()
			continue
		}
		<-ticker.C // if the handler takes too long, the next tick will arrive at once

		newDuration := curDuration
		if d.SegLen < curDuration {
			if ret := curDuration - d.SegLen; ret/d.SegLen > 0.18 && ret > 0.6 {
				newDuration = (d.SegLen + curDuration) / 2
				if newDuration < 1.2 {
					newDuration = 1.2
				}
				d.Logger.Infof("Using new hls interval: %f", newDuration*0.8)
			}
			shakeCount = 0
		} else {
			if ret := math.Abs(d.SegLen - curDuration); ret/d.SegLen > 0.18 && ret > 0.8 {
				shakeCount += 1
				if shakeCount >= 3 { // segLen are more keen to go lower
					newDuration = (d.SegLen + curDuration) / 2
					if newDuration < 1.2 {
						newDuration = 1.2
					}
				}
			} else {
				shakeCount = 0
			}
		}
		if newDuration != curDuration {
			ticker.Stop()
			curDuration = newDuration
			d.Logger.Infof("Using new hls interval: %f", curDuration*0.7)
			ticker = time.NewTicker(time.Duration(float64(time.Second) * curDuration * 0.7))
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
				d.refreshClient()
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
				d.Logger.WithError(err).Debugf("Streamlink requests to abort, worker finishing...")
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
//func (d *HLSDownloader) Writer_old() {
//	getMinNo := func() int {
//		minNo := -1
//		d.SeqMap.Range(func(key, value interface{}) bool {
//			cur := key.(int)
//			if minNo == -1 {
//				minNo = cur
//			}
//			if key.(int) < minNo {
//				minNo = cur
//			}
//			return true
//		})
//		return minNo
//	}
//	getMinNo()
//
//	lastArrivalTime := time.Now()
//
//	writeSeg := func(curSeq int, val *HLSSegment) {
//		timeoutChan := make(chan int, 1)
//		go func(timeoutChan chan int, startTime time.Time, segNo int) {
//			// detect writing timeout
//			timer := time.NewTimer(15 * time.Second)
//			select {
//			case <-timeoutChan:
//				d.Logger.Debugf("Wrote segment %d in %s", segNo, time.Now().Sub(startTime))
//			case <-timer.C:
//				d.Logger.Warnf("Write segment %d too slow...", curSeq)
//				timer2 := time.NewTimer(60 * time.Second)
//				select {
//				case <-timeoutChan:
//					d.Logger.Debugf("Wrote segment %d in %s", segNo, time.Now().Sub(startTime))
//				case <-timer2.C:
//					d.Logger.Errorf("Write segment %d timeout!!!!!!!", curSeq)
//				}
//			}
//		}(timeoutChan, time.Now(), curSeq)
//		_, err := d.output.Write(val.Data.Bytes())
//		timeoutChan <- 1
//
//		//bufPool.Put(val.Data)
//		val.Data = nil
//		if err != nil {
//			d.sendErr(err)
//			return
//		}
//	}
//
//	// get the seq of first segment, then start the writing
//	curSeq := <-d.firstSeqChan
//	for {
//		// calculate the load time, so that we can check the timeout
//		loadTime := time.Second * 0
//		//d.Logger.Debugf("Loading segment %d", curSeq)
//		for {
//			_val, ok := d.SeqMap.Load(curSeq)
//			if ok {
//				// the segment has already been retrieved
//				val := _val.(*HLSSegment)
//
//				if val.Data != nil {
//					// segment has been downloaded
//					if curSeq >= 60 {
//						d.SeqMap.Range(func(key, value interface{}) bool {
//							if key.(int) <= curSeq-20 && value.(*HLSSegment).SegArriveTime.Before(time.Now().Add(- time.Second * 180)) {
//								d.SeqMap.Delete(key.(int))
//							}
//							return true
//						})
//					}
//					lastArrivalTime = val.SegArriveTime
//					writeSeg(curSeq, val)
//					break
//				}
//				// segment still not downloaded, increase the load time
//			} else {
//				if loadTime > 20*time.Second {
//					// segment is not loaded
//					{
//						// detect seqNo got reset to 1
//						minNo := 2147483647
//						d.SeqMap.Range(func(key, value interface{}) bool {
//							if value.(*HLSSegment).SegArriveTime.After(lastArrivalTime) {
//								if key.(int) < minNo {
//									minNo = key.(int)
//								}
//							}
//							return true
//						})
//						reset := false
//						if _minSeg, ok := d.SeqMap.Load(minNo); ok {
//							minSeg := _minSeg.(*HLSSegment)
//							if minSeg.SegArriveTime.Sub(lastArrivalTime) < 15 * time.Second {
//								reset = true
//							}
//						}
//						if (d.lastSeqNo >= 3 && d.lastSeqNo+30 < curSeq) {
//							reset = true
//						}
//						if reset && minNo != 2147483647 {
//							hasLargerId := false
//							/*
//							d.SeqMap.Range(func(key, value interface{}) bool {
//								if key.(int) > curSeq {
//									hasLargerId = true
//									return false
//								} else {
//									return true
//								}
//							})*/
//							if !hasLargerId {
//								d.Logger.Warnf("Failed to load segment %d due to segNo got reset to %d", curSeq, minNo)
//								curSeq = minNo
//								continue
//								// exit ASAP so that alt stream will be preserved
//								//d.sendErr(fmt.Errorf("Failed to load segment %d due to segNo got reset to %d", curSeq, d.lastSeqNo))
//								//return
//							} else {
//								// fall into lag check instead
//							}
//						}
//					}
//
//					{
//						// detect if we are lagged (e.g. we are currently at seg2, still waiting for seg3 to appear, however seg4 5 6 7 has already been downloaded)
//						isLagged := false
//						d.SeqMap.Range(func(key, value interface{}) bool {
//							if key.(int) > curSeq+4 && value.(*HLSSegment).Data != nil {
//								d.Logger.Warnf("curSeq %d lags behind segData %d!", curSeq, key.(int))
//								isLagged = true
//								return false
//							} else {
//								return true
//							}
//						})
//						if isLagged { // exit ASAP so that alt stream will be preserved
//							//d.sendErr(fmt.Errorf("Failed to load segment %d within m3u8 timeout due to lag...", curSeq))
//							nextSeq := 2147483647
//							d.SeqMap.Range(func(key, value interface{}) bool {
//								if key.(int) > curSeq && key.(int) < nextSeq {
//									nextSeq = key.(int)
//								}
//								return true
//							})
//							d.Logger.Warnf("Failed to load segment %d within m3u8 timeout due to lag, continuing to next seg %d", curSeq, nextSeq)
//							curSeq = nextSeq
//							continue
//						}
//					}
//				}
//			}
//			time.Sleep(500 * time.Millisecond)
//			loadTime += 500 * time.Millisecond
//			// if load time is too long, then likely the recording is interrupted
//			if loadTime == 1*time.Minute || loadTime == 150*time.Second || loadTime == 240*time.Second {
//				//go d.AltSegDownloader() // trigger alt download in advance, so we can avoid more loss
//			}
//			if loadTime > 2*time.Minute { // segNo shouldn't return to 0 within 5 min
//				// if we come to here, then the lag detect would already failed, the live must be interrupted
//				newSeq := 2147483647
//				d.SeqMap.Range(func(key, value interface{}) bool {
//					if key.(int) > curSeq && key.(int) < newSeq {
//						newSeq = key.(int)
//					}
//					return true
//				})
//				if newSeq != 2147483647 {
//					d.Logger.Warnf("Failed to load segment %d within timeout, continuing to segment %d", curSeq, newSeq)
//					curSeq = newSeq
//					continue
//				} else {
//					d.sendErr(fmt.Errorf("Failed to load segment %d within timeout...", curSeq))
//					return
//				}
//			}
//			if curSeq == d.FinishSeq { // successfully finished
//				d.sendErr(nil)
//				return
//			}
//		}
//		curSeq += 1
//	}
//}

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
	getMinNo()

	writeSeg := func(curSeq int, val *HLSSegment) bool {
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
		d.writeLagNum.Dec()
		d.lastWriteSeqNo = curSeq
		timeoutChan <- 1

		//bufPool.Put(val.Data)
		val.Data = nil
		if err != nil {
			d.sendErr(err)
			return false
		}
		return true
	}

	/*type WriteSegReq struct {
		curSeq		int
		val			*HLSSegment
	}
	segChan := make(chan WriteSegReq, 10)
	defer close(segChan)
	go func() {
		for {
			segWrite := <-segChan
			if segWrite.curSeq == 0 && segWrite.val == nil { // chan closed
				break
			}
			writeSeg(segWrite.curSeq, segWrite.val)
		}
	}()*/

	type LoadStatus int32
	const (
		WRITE_FAILED  LoadStatus = 3
		LOAD_FINISHED LoadStatus = 2
		DOWNLOAD_WAIT LoadStatus = 1
		WAIT_SEG      LoadStatus = 0
	)

	lastArrivalTime := time.Now().Add(-200 * time.Second)
	doLoad := func(curSeq int) LoadStatus {
		_val, ok := d.SeqMap.Load(curSeq)
		if ok {
			// the segment has already been retrieved
			val := _val.(*HLSSegment)

			if val.Data != nil {
				// segment has been downloaded
				if curSeq >= 60 {
					d.SeqMap.Range(func(key, value interface{}) bool {
						// delete old entry, but preserve reseted seg & recently segs (so that seg won't be reload)
						if /*key.(int) <= curSeq-20 && */ value.(*HLSSegment).SegArriveTime.Before(time.Now().Add(-time.Second * 240)) {
							d.SeqMap.Delete(key.(int))
						}
						return true
					})
				}
				lastArrivalTime = val.SegArriveTime
				//segChan <- WriteSegReq{curSeq, val}
				if writeSeg(curSeq, val) {
					return LOAD_FINISHED
				} else {
					return WRITE_FAILED
				}
			}
			// segment still not downloaded, increase the load time
			return DOWNLOAD_WAIT
		} else {
			return WAIT_SEG
		}
	}
	// get the seq of first segment, then start the writing
	curSeq := <-d.firstSeqChan
	for {
		// calculate the load time, so that we can check the timeout
		loadTime := time.Second * 0
		//d.Logger.Debugf("Loading segment %d", curSeq)
		for {
			segChoices := make([]*HLSSegment, 0)
			{
				// add possible segs
				d.SeqMap.Range(func(key, value interface{}) bool {
					val := value.(*HLSSegment)
					if val.SegArriveTime.After(lastArrivalTime) {
						segChoices = append(segChoices, val)
					}
					return true
				})
			}
			sort.Slice(segChoices, func(i, j int) bool {
				if segChoices[i].SegArriveTime.Equal(segChoices[j].SegArriveTime) {
					return segChoices[i].SegNo < segChoices[j].SegNo
				}
				return segChoices[i].SegArriveTime.Before(segChoices[j].SegArriveTime)
			})
			nextSeq := -1
			if len(segChoices) == 0 {
				nextSeq = curSeq
			} else {
				nextSeq = segChoices[0].SegNo
				for _, seg := range segChoices {
					if seg.SegNo == curSeq {
						nextSeq = curSeq
					}
				}
			}
			ret := doLoad(nextSeq)
			if ret == WRITE_FAILED {
				return
			} else if ret == LOAD_FINISHED {
				if nextSeq != curSeq {
					d.Logger.Warnf("segNo jumped from %d to %d", curSeq, nextSeq)
				}
				curSeq = nextSeq
				d.writeLagNum.Store(0)
				break
			} else if ret == DOWNLOAD_WAIT {
				// simply wait
			} else if ret == WAIT_SEG {
				// handled above
			}
			time.Sleep(500 * time.Millisecond)
			loadTime += 500 * time.Millisecond
			// if load time is too long, then likely the recording is interrupted
			if loadTime == 1*time.Minute || loadTime == 150*time.Second || loadTime == 240*time.Second {
				//go d.AltSegDownloader() // trigger alt download in advance, so we can avoid more loss
			}

			var val *HLSSegment
			if _val, ok := d.SeqMap.Load(nextSeq); ok {
				if val, ok = _val.(*HLSSegment); ok {

				}
			}
			if d.writeLagNum.Load() > 10 || loadTime > 2*time.Minute || (val != nil && time.Now().Sub(val.SegArriveTime) > 120*time.Second) { // segNo shouldn't return to 0 within 5 min
				if val != nil {
					val.SegConCancel()
				}
				// if we come to here, then the lag detect would already failed, the live must be interrupted
				newSeq := 2147483647
				d.SeqMap.Range(func(key, value interface{}) bool {
					if key.(int) > curSeq && key.(int) < newSeq {
						newSeq = key.(int)
					}
					return true
				})
				if newSeq != 2147483647 {
					d.Logger.Warnf("Failed to load segment %d within timeout, continuing to segment %d", curSeq, newSeq)
					d.writeLagNum.Store(0)
					curSeq = newSeq
					loadTime = time.Second * 0
					continue
				} else {
					d.sendErr(fmt.Errorf("Failed to load segment %d within timeout...", curSeq))
					return
				}
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
	d.initLBData()
	// rate limit, so we won't break up all things
	//d.segRl = ratelimit.New(1)

	//d.segRl = rate.NewLimiter(rate.Every(time.Second*5), 1)
	if strings.Contains(d.HLSUrl, "gotcha105") {
		//d.segRl = semaphore.NewWeighted(1)
		d.segRl = utils.NewPrioritySem(1)
	} else {
		//d.segRl = semaphore.NewWeighted(2)
		d.segRl = utils.NewPrioritySem(5)
	}

	d.SegLen = 2.0
	d.segLens = ring.New(4)

	writer := utils.GetWriter(d.OutPath)
	d.output = writer
	defer writer.Close()

	d.useH2 = false
	if strings.Contains(d.HLSUrl, "gotcha105") {
		d.useH2 = true
	}

	d.allClients = make([]*http.Client, 0)
	d.allClients = append(d.allClients, d.Clients...)
	d.allClients = append(d.allClients, d.AltClients...)

	d.segClients = make([]*http.Client, 0)
	for _, cli := range d.Clients {
		d.segClients = append(d.segClients, utils.CloneHttpClient(cli, nil))
		d.h2Clients = append(d.h2Clients, utils.CloneHttpClient(cli, func(transport *http.Transport) http.RoundTripper {
			return stealth.HTTP2Trans
		}))
	}

	d.clientsMap = make(map[*http.Client]string)
	for _, client := range d.Clients {
		d.clientsMap[client] = "main"
	}
	for _, client := range d.segClients {
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

	if false && !d.AltAsMain && d.hasAlt {
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

type LBData struct {
	mu   sync.RWMutex
	data map[string]string
}

func (d *HLSDownloader) initLBData() {
	lbData := d.loadBalanceData
	lbData.mu.Lock()
	defer lbData.mu.Unlock()
	advSettings := config.Config.AdvancedSettings
	for k, v := range advSettings.DomainRewrite {
		lbData.data[k] = utils.RandChooseStr(v)
	}
	d.Logger.Infof("Initiated loadBalanceData: %v", lbData.data)
}

// initialize the go hls downloader
func (dd *DownloaderGo) doDownloadHls(entry *log.Entry, output string, video *interfaces.VideoInfo, m3u8url string, headers map[string]string, needMove bool) error {
	lbData := &LBData{data: make(map[string]string)}

	clients := []*http.Client{
		{
			Transport: &http.Transport{
				MaxIdleConnsPerHost:   8000,
				IdleConnTimeout:       14 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
				TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
				DisableCompression:    true,
				DisableKeepAlives:     false,
				TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
				DialContext: func(ctx context.Context, network, addr string) (conn net.Conn, err error) {
					lbData.mu.RLock()
					_addr := addr
					if domain, ok := lbData.data[addr]; ok {
						addr = domain
					}
					lbData.mu.RUnlock()
					if logger := ctx.Value("logger"); logger != nil {
						logger.(*log.Entry).WithError(err).Tracef("Connecting %s %s with %s", network, _addr, addr)
					}
					return http.DefaultTransport.(*http.Transport).DialContext(ctx, network, addr)
				},
				DialTLS: http.DefaultTransport.(*http.Transport).DialTLS,
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
					MaxIdleConnsPerHost: 2,
					TLSNextProto:        make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
					Proxy:               http.ProxyURL(proxyUrl),
					//DisableCompression: true,
					DisableKeepAlives: true,
					TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
				},
				Timeout: 100 * time.Second,
			},
		}
	} else {
		altclients = []*http.Client{}
	}

	d := &HLSDownloader{
		Context:            context.Background(),
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
		loadBalanceData:    lbData,
		//output:    out,
	}
	err := d.startDownload()
	time.Sleep(1 * time.Second)
	utils.ExecShell("/home/misty/rclone", "rc", "vfs/forget", "dir="+path.Dir(output))
	return err
}
