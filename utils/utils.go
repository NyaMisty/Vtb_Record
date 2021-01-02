package utils

import (
	"bytes"
	"context"
	"fmt"
	"github.com/mitchellh/mapstructure"
	log "github.com/sirupsen/logrus"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

//var client *http.Client

func init() {
	//client = createClient()
}

func MapToStruct(mapVal map[string]interface{}, structVal interface{}) error {
	config := &mapstructure.DecoderConfig{
		Metadata:         nil,
		Result:           structVal,
		WeaklyTypedInput: true,
	}
	decoder, err := mapstructure.NewDecoder(config)
	if err != nil {
		return err
	}
	decoder.Decode(mapVal)
	return nil
}

func HttpGetBuffer(client *http.Client, url string, header map[string]string, buf *bytes.Buffer) (*bytes.Buffer, error) {
	return HttpDoWithBufferEx(context.Background(), client, "GET", url, header, nil, buf)
}

func HttpDo(ctx context.Context, client *http.Client, meth string, url string, header map[string]string, data []byte) (*http.Response, error) {
	if client == nil {
		client = &http.Client{}
	}
	var dataReader io.Reader
	if data != nil {
		dataReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, meth, url, dataReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/75.0.3770.142 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	for k, v := range header {
		req.Header.Set(k, v)
	}
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

func HttpMultiDownload(ctx context.Context, client *http.Client, meth, url string, header map[string]string, data []byte, chunkSize int64) ([]byte, error) {
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

	if length < chunkSize {
		chunkSize = length
	}
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
			for {
				if curTime > 10 {
					log.WithError(err).Warnf("Failed to download chunk %d for %s", i, url)
					retErr = fmt.Errorf("download chunk %d failed", i)
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
				res, err = HttpDo(ctx, client, meth, url, curHdr, data)

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
					time.Sleep(time.Second * 2)
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

func IsFileExist(aFilepath string) bool {
	if _, err := os.Stat(aFilepath); err == nil {
		return true
	} else {
		//log.Errorf("File not exist %s, stat err %s", aFilepath, err)
		return false
	}
}
func GenerateFilepath(DownDir string, VideoTitle string) string {
	pathSlice := []string{DownDir, VideoTitle}
	aFilepath := strings.Join(pathSlice, "/")
	/*if IsFileExist(aFilepath) {
		return ChangeName(aFilepath)
	} else {
		return aFilepath
	}*/
	return ChangeName(aFilepath)
}
func MakeDir(dirPath string) (ret string, err error) {
	//if !IsFileExist(dirPath) {
	if true {
		//err := os.MkdirAll(dirPath, 0775)
		err = MkdirAll(dirPath)
		if err != nil {
			log.Errorf("mkdir error: %s, err: %s", dirPath, err)
			ret = ""
			return
		}
		return dirPath, nil
	}
	return dirPath, nil
}

func AddSuffix(aFilepath string, suffix string) string {
	dir, file := filepath.Split(aFilepath)
	ext := path.Ext(file)
	filename := strings.TrimSuffix(path.Base(file), ext)
	filename += "_"
	filename += suffix
	return dir + filename + ext
}

func ChangeName(aFilepath string) string {
	return AddSuffix(aFilepath, strconv.FormatInt(time.Now().Unix(), 10))
}
func GetTimeNow() string {
	return time.Now().Format("2006-01-02 15:04:05")
}
func RemoveIllegalChar(Title string) string {
	illegalChars := []string{"|", "/", "\\", ":", "?"}
	//"github.com/fzxiao233/Go-Emoji-Utils"
	//Title = emoji.RemoveAll(Title)
	for _, char := range illegalChars {
		Title = strings.ReplaceAll(Title, char, "#")
	}
	return Title
}

func I2b(i int) bool {
	if i != 0 {
		return true
	} else {
		return false
	}
}

func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func Max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func RPartition(s string, sep string) (string, string, string) {
	parts := strings.SplitAfter(s, sep)
	if len(parts) == 1 {
		return "", "", parts[0]
	}
	return strings.Join(parts[0:len(parts)-1], ""), sep, parts[len(parts)-1]
}

func RandChooseStr(arr []string) string {
	return arr[rand.Intn(len(arr))]
}

func GenRandBuf(p []byte) (n int, err error) {
	r := rand.NewSource(time.Now().Unix())
	todo := len(p)
	offset := 0
	for {
		val := int64(r.Int63())
		for i := 0; i < 8; i++ {
			p[offset] = byte(val & 0xff)
			todo--
			if todo == 0 {
				return len(p), nil
			}
			offset++
			val >>= 8
		}
	}

	panic("unreachable")
}
