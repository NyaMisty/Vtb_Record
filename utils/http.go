package utils

import (
	"bytes"
	"context"
	"fmt"
	"golang.org/x/sync/semaphore"
	"io"
	"io/ioutil"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

func CloneHttpClient(client *http.Client, customizer func(transport *http.Transport) http.RoundTripper) *http.Client {
	oriTrans, ok := client.Transport.(*http.Transport)
	if !ok {
		return client
	}
	_newTrans := oriTrans.Clone()
	var newTrans http.RoundTripper = _newTrans
	if customizer != nil {
		newTrans = customizer(_newTrans)
	}
	return &http.Client{
		Timeout:   client.Timeout,
		Transport: newTrans,
	}
}

func HttpGetBuffer(client *http.Client, url string, header map[string]string, buf *bytes.Buffer) (*bytes.Buffer, error) {
	return HttpDoWithBufferEx(context.Background(), client, "GET", url, header, nil, buf)
}

func HttpBuildRequest(ctx context.Context, meth string, url string, header map[string]string, data []byte) (*http.Request, error) {
	var dataReader io.Reader
	if data != nil {
		dataReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, meth, url, dataReader)
	if err != nil {
		return nil, err
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/75.0.3770.142 Safari/537.36")
		req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	} else if req.Header.Get("User-Agent") == "nil" {
		req.Header.Del("User-Agent")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return req, err
}

func HttpDoRequest(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	res, err := client.Do(req)
	if err != nil || res == nil {
		err = fmt.Errorf("HttpGet error %w", err)
		//log.Warn(err)
		return res, err
	}

	if res.StatusCode != 200 && res.StatusCode != 206 {
		if res.StatusCode == 404 {
			err = fmt.Errorf("HttpGet status error %d", res.StatusCode)
			return res, err
		} else {
			//buf := bytes.NewBuffer(make([]byte, 0))
			//_, _ = io.Copy(buf, res.Body)
			err = fmt.Errorf("HttpGet status error %d with header %v", res.StatusCode, req.Header) //, string(buf.Bytes()))
			//log.Warn(err)
			return res, err
		}
	}
	return res, nil
}

func HttpDo(ctx context.Context, client *http.Client, meth string, url string, header map[string]string, data []byte) (*http.Response, error) {
	if client == nil {
		client = &http.Client{}
	}
	req, err := HttpBuildRequest(ctx, meth, url, header, data)
	if err != nil {
		return nil, err
	}
	return HttpDoRequest(ctx, client, req)
}

func HttpRawDoWithBufferEx(ctx context.Context, client *http.Client, meth string, url string, header map[string]string, data []byte, buf io.Writer) (*bytes.Buffer, *http.Response, error) {
	res, err := HttpDo(ctx, client, meth, url, header, data)
	if err != nil {
		return nil, res, err
	}
	if res != nil {
		defer res.Body.Close()
	}

	//log.Infof("%v", res.ContentLength)
	//var htmlBody []byte
	var buf_ *bytes.Buffer
	buf_, ok := buf.(*bytes.Buffer)
	if !ok {
		buf_ = nil
	}
	if buf == nil || (reflect.ValueOf(buf).Kind() == reflect.Ptr && reflect.ValueOf(buf).IsNil()) {
		buf_ = bytes.NewBuffer(make([]byte, Max(res.ContentLength, 2048)))
		buf = buf_
	}
	if res.ContentLength >= 0 {
		if buf_ != nil {
			buf_.Reset()
			if int64(buf_.Cap()) < res.ContentLength {
				buf_.Grow(int(res.ContentLength) - buf_.Cap())
			}
			buf = buf_
		}
		//buf := bytes.NewBuffer(make([]byte, 0, res.ContentLength))
		n, err := io.Copy(buf, res.Body)
		if err != nil {
			return nil, res, err
		}
		if n != res.ContentLength {
			return nil, res, fmt.Errorf("Got unexpected payload: expected: %v, got %v", res.ContentLength, n)
		}
		//htmlBody = buf.Bytes()
	} else {
		if buf_ != nil {
			buf_.Reset()
			buf = buf_
		}
		_, err := io.Copy(buf, res.Body)
		if err != nil {
			return nil, res, err
		}
		//htmlBody, _ = ioutil.ReadAll(res.Body)
	}
	return buf_, res, nil
}

func HttpDoWithBufferEx(ctx context.Context, client *http.Client, meth string, url string, header map[string]string, data []byte, buf io.Writer) (*bytes.Buffer, error) {
	newbuf, _, err := HttpRawDoWithBufferEx(ctx, client, meth, url, header, data, buf)
	return newbuf, err
}

func HttpGet(client *http.Client, url string, header map[string]string) ([]byte, error) {
	buf, err := HttpGetBuffer(client, url, header, nil)
	if err != nil {
		return nil, err
	} else {
		return buf.Bytes(), nil
	}
}

func HttpPost(client *http.Client, url string, header map[string]string, data []byte) ([]byte, error) {
	buf, err := HttpDoWithBufferEx(context.Background(), client, "POST", url, header, data, nil)
	if buf == nil {
		return nil, err
	} else {
		return buf.Bytes(), err
	}
}

var totalSem = semaphore.NewWeighted(4200)

func HttpMultiDownload(ctx context.Context, client *http.Client, meth, url string, header map[string]string, data []byte, chunkSize int64) ([]byte, error) {
	startTime := time.Now()
	var length int64 = 0
	if false {
		tempCli := CloneHttpClient(client, func(transport *http.Transport) http.RoundTripper {
			transport.DisableKeepAlives = true
			return transport
		})
		res, err := HttpDo(ctx, tempCli, meth, url, header, data)
		if res != nil {
			_ = res.Body.Close()
		}
		if err != nil {
			return nil, err
		}
		if hdrs, ok := res.Header["Content-Length"]; ok {
			if len(hdrs) != 0 {
				length, err = strconv.ParseInt(hdrs[0], 10, 64)
			}
		}
	} else {
		newHdr := make(map[string]string)
		for k, v := range header {
			newHdr[k] = v
		}
		newHdr["Range"] = "bytes=0-0"
		res, err := HttpDo(ctx, client, meth, url, newHdr, data)
		if res != nil {
			_, _ = io.CopyN(ioutil.Discard, res.Body, 2)
			_ = res.Body.Close()
		}
		if err != nil {
			return nil, err
		}
		if hdrs, ok := res.Header["Content-Range"]; ok {
			if len(hdrs) != 0 {
				if _tmp := strings.Split(hdrs[0], "/"); len(_tmp) == 2 {
					length, err = strconv.ParseInt(_tmp[1], 10, 64)
				}
			}
		}
	}
	if length <= 0 {
		return nil, fmt.Errorf("cannot get target file size")
	}

	thread := int(length / chunkSize)
	if thread == 0 {
		thread = 1
	}

	if length < chunkSize {
		chunkSize = length
	}
	segTimeout := time.Second * 15

	sem := semaphore.NewWeighted(10)

	ret := make([]byte, length)
	var wg sync.WaitGroup
	var retErr error = nil

	for i := 0; i < thread; i++ {
		wg.Add(1)
		min := chunkSize * int64(i)   // Min range
		max := chunkSize * int64(i+1) // Max range
		if i == thread-1 {
			max = -1
		}
		downloadChunk := func(min int64, max int64, i string) {
			if err := totalSem.Acquire(ctx, 1); err == nil {
				defer totalSem.Release(1)
			}
			if err := sem.Acquire(ctx, 1); err == nil {
				defer sem.Release(1)
			} else {
				wg.Done()
				retErr = fmt.Errorf("failed to acquire sem, err: %v", err)
				return
			}
			curTime := 0
			errs := make([]error, 0)
			for {
				if time.Now().Sub(startTime) > 60*time.Second {
					//log.Warnf("Failed to download chunk %d for %s, errors: %v", i, url, errs)
					retErr = fmt.Errorf("download chunk %d-%d for %s failed, errors: %v", min, max, url, errs)
					break
				}
				curTime++
				curHdr := make(map[string]string)
				for k, v := range header {
					curHdr[k] = v
				}
				var bytesrange string
				if max != -1 {
					bytesrange = "bytes=" + strconv.FormatInt(min, 10) + "-" + strconv.FormatInt(max-1, 10)
				} else {
					bytesrange = "bytes=" + strconv.FormatInt(min, 10) + "-"
				}

				curHdr["Range"] = bytesrange
				curCtx, _ := context.WithTimeout(ctx, segTimeout)
				res, err := HttpDo(curCtx, client, meth, url, curHdr, data)

				if err == nil && res.StatusCode != 206 {
					err = fmt.Errorf("unknown status code %d for multi-down", res.StatusCode)
				}

				if strings.Contains(err.Error(), "HttpGet status error") {
					retErr = err
					break
				}

				readed := -1
				if err == nil {
					if retRange := res.Header.Get("Content-Range"); retRange != "" {
						if rangeParsed := strings.Split(retRange, "/"); len(rangeParsed) == 2 {
							if fullLen, err := strconv.ParseInt(rangeParsed[1], 10, 64); err == nil {
								if fullLen != length {
									err = fmt.Errorf("full length changed from %v to %v, aborting", length, fullLen)
								}
							} else {
								err = fmt.Errorf("failed to parse Content-Range: cannot parse full length %v, error %s", rangeParsed[1], err)
							}
						} else {
							err = fmt.Errorf("failed to parse Content-Range: invalid range: %s", retRange)
						}
					} else {
						err = fmt.Errorf("missing Content-Range in response")
					}
					if err != nil { // invalid return format, fail at once
						retErr = err
						break
					}
					targetSlice := ret[min:]
					if max != -1 {
						targetSlice = ret[min:max]
					}
					readed, err = io.ReadFull(res.Body, targetSlice)
					if readed != -1 {
						min += int64(readed)
					}
				}
				if res != nil {
					_ = res.Body.Close()
				}

				if err != nil {
					/*if logger := ctx.Value("logger"); logger != nil {
						logger.(*log.Entry).WithError(err).Debugf("Failed to download chunk %d for %s", i, url)
					}*/
					time.Sleep(time.Second * 2)
					errs = append(errs, err)
					continue
				}
				break
			}
			wg.Done()
		}
		go downloadChunk(min, max, strconv.Itoa(i))
	}
	wg.Wait()
	return ret, retErr
}

func HttpMultiDownload1(ctx context.Context, client *http.Client, meth, url string, header map[string]string, data []byte, chunkSize int64) ([]byte, error) {
	res, err := HttpDo(ctx, client, meth, url, header, data)
	if res != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	var length int64 = 0
	if hdrs, ok := res.Header["Content-Length"]; ok {
		if len(hdrs) != 0 {
			length, err = strconv.ParseInt(hdrs[0], 10, 64)
		}
	}
	if length <= 0 {
		return nil, fmt.Errorf("cannot get target file size")
	}

	thread := int(length / chunkSize)
	if thread == 0 {
		thread = 1
	}
	if thread > 16 {
		thread = 16
	}

	if length < chunkSize {
		chunkSize = length
	}
	segTimeout := time.Second * 30
	oriChunkSize := chunkSize
	chunkSize = length / int64(thread)
	if oriChunkSize == 0 {
		oriChunkSize = chunkSize
	}
	segTimeout = time.Duration(chunkSize/oriChunkSize) * segTimeout
	ret := make([]byte, length)
	var wg sync.WaitGroup
	var retErr error = nil

	for i := 0; i < thread; i++ {
		wg.Add(1)
		min := chunkSize * int64(i)   // Min range
		max := chunkSize * int64(i+1) // Max range
		if i == thread-1 {
			max = -1
		}
		go func(min int64, max int64, i int) {
			curTime := 0
			errs := make([]error, 0)
			for {
				if curTime > 4 {
					//log.Warnf("Failed to download chunk %d for %s, errors: %v", i, url, errs)
					retErr = fmt.Errorf("download chunk %d for %s failed, errors: %v", i, url, errs)
					break
				}
				curTime++
				curHdr := make(map[string]string)
				for k, v := range header {
					curHdr[k] = v
				}
				var bytesrange string
				if max != -1 {
					bytesrange = "bytes=" + strconv.FormatInt(min, 10) + "-" + strconv.FormatInt(max-1, 10)
				} else {
					bytesrange = "bytes=" + strconv.FormatInt(min, 10) + "-"
				}

				curHdr["Range"] = bytesrange
				curCtx, _ := context.WithTimeout(ctx, segTimeout)
				res, err = HttpDo(curCtx, client, meth, url, curHdr, data)

				if err == nil {
					targetSlice := ret[min:]
					if max != -1 {
						targetSlice = ret[min:max]
					}
					_, err = io.ReadFull(res.Body, targetSlice)
				}
				if res != nil {
					_ = res.Body.Close()
				}

				if err != nil {
					//log.WithError(err).Debugf("Failed to download chunk %d for %s", i, url)
					//time.Sleep(time.Second * 2)
					errs = append(errs, err)
					continue
				}
				break
			}
			wg.Done()
		}(min, max, i)
	}
	wg.Wait()
	return ret, retErr
}
