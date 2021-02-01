package utils

import (
	"context"
	_ "github.com/rclone/rclone/backend/all"
	"github.com/rclone/rclone/cmd"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/sync"
	log "github.com/sirupsen/logrus"
	"io"
	"time"
)

type FileType int

const (
	FILE_NORMAL FileType = 0
	FILE_RCLONE FileType = 1
)

type FsType int

const (
	FS_OS     FsType = 0
	FS_RCLONE FsType = 1
)

type FileSystem struct {
	FsType FsType
	FsData interface{}
}

type FsRcloneData struct {
	rcloneFs *fs.Fs
}

func MkdirAll(path string) error {
	fdst := cmd.NewFsDir([]string{path})
	err := operations.Mkdir(context.Background(), fdst, "")
	return err
}

func MoveFiles(src string, dst string) error {
	fsrc, srcFileName, fdst := cmd.NewFsSrcFileDst([]string{src, dst})
	if srcFileName == "" {
		return sync.MoveDir(context.Background(), fdst, fsrc, false, false)
	}
	return operations.MoveFile(context.Background(), fdst, fsrc, srcFileName, srcFileName)
}

type PaddingReader struct {
	WrapReader io.ReadCloser
	PaddingLen int64
	hasEof     bool
	cur        int64
}

func (r *PaddingReader) Read(p []byte) (n int, err error) {
	if !r.hasEof {
		n, err = r.WrapReader.Read(p)
		r.cur += int64(n)
		if err == io.EOF {
			r.hasEof = true
			r.WrapReader.Close()
			return n, nil
		}
		return n, err
	} else {
		readLen := int64(len(p))
		if readLen > r.PaddingLen-r.cur {
			readLen = r.PaddingLen - r.cur
			err = io.EOF
		}
		r.cur += readLen
		for i := range p[:readLen] {
			p[i] = 0
		}
		return n, err
	}
}

func (r *PaddingReader) Close() error {
	return nil
}

var FILE_SIZE int64 = 4 * 1024 * 1024 * 1024

func GetWriter(dst string) io.WriteCloser {
	reader, writer := io.Pipe()
	fdst, dstFileName := cmd.NewFsDstFile([]string{dst})
	go func() {
		var err error
		if fdst.Features().PutStream != nil {
			_, err = operations.Rcat(context.Background(), fdst, dstFileName, reader, time.Now())
		} else {
			reader_pad := &PaddingReader{
				WrapReader: reader,
				PaddingLen: FILE_SIZE,
			}
			_, err = operations.RcatSize(context.Background(), fdst, dstFileName, reader_pad, FILE_SIZE, time.Now())
		}
		if err != nil {
			log.Warnf("Rcat [%s] Error! err: %s", dst, err)
		}
		_ = reader.Close()
		_ = writer.Close()
	}()
	return writer
}
