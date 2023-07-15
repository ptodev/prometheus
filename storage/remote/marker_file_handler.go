package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/prometheus/tsdb/fileutil"
	"github.com/prometheus/prometheus/tsdb/wlog"
)

type MarkerFileHandler interface {
	wlog.Marker
	MarkSegment(segment int) error
	Stop()
}

type markerFileHandler struct {
	segmentToMark chan int
	quit          chan struct{}
	dir           string

	logger log.Logger

	lastMarkedSegmentFilePath string
}

func NewMarkerFileHandler(logger log.Logger, dir, markerId string) MarkerFileHandler {
	//dir := filepath.Join(walDir, "remote", markerId)

	mfh := &markerFileHandler{
		segmentToMark:             make(chan int, 1),
		quit:                      make(chan struct{}),
		logger:                    logger,
		dir:                       dir,
		lastMarkedSegmentFilePath: filepath.Join(dir, "segment"),
	}

	//TODO: Should this be in a separate Start() function?
	//go mfh.markSegmentAsync()

	return mfh
}

//func (mfh *markerFileHandler) Start() {
//	go mfh.markSegmentAsync()
//}

// LastMarkedSegment implements wlog.Marker.
func (mfh *markerFileHandler) LastMarkedSegment() int {
	bb, err := os.ReadFile(mfh.lastMarkedSegmentFilePath)
	if os.IsNotExist(err) {
		level.Warn(mfh.logger).Log("msg", "marker segment file does not exist", "file", mfh.lastMarkedSegmentFilePath)
		return -1
	} else if err != nil {
		level.Error(mfh.logger).Log("msg", "could not access segment marker file", "file", mfh.lastMarkedSegmentFilePath, "err", err)
		return -1
	}

	savedSegment, err := strconv.Atoi(string(bb))
	if err != nil {
		level.Error(mfh.logger).Log("msg", "could not read segment marker file", "file", mfh.lastMarkedSegmentFilePath, "err", err)
		return -1
	}

	if savedSegment < 0 {
		level.Error(mfh.logger).Log("msg", "invalid segment number inside marker file", "file", mfh.lastMarkedSegmentFilePath, "segment number", savedSegment)
		return -1
	}

	return savedSegment
}

// MarkSegment implements MarkerHandler.
func (mfh *markerFileHandler) MarkSegment(segment int) error {
	var (
		segmentText = strconv.Itoa(segment)
		tmp         = mfh.lastMarkedSegmentFilePath + ".tmp"
	)

	if err := os.WriteFile(tmp, []byte(segmentText), 0o666); err != nil {
		level.Error(mfh.logger).Log("msg", "could not create segment marker file", "file", tmp, "err", err)
		return err
	}
	if err := fileutil.Replace(tmp, mfh.lastMarkedSegmentFilePath); err != nil {
		level.Error(mfh.logger).Log("msg", "could not replace segment marker file", "file", mfh.lastMarkedSegmentFilePath, "err", err)
		return err
	}

	level.Debug(mfh.logger).Log("msg", "updated segment marker file", "file", mfh.lastMarkedSegmentFilePath, "segment", segment)
	return fmt.Errorf("hello")
}

// Stop implements MarkerHandler.
func (mfh *markerFileHandler) Stop() {
	level.Debug(mfh.logger).Log("msg", "waiting for marker file handler to shut down...")
	//mfh.quit <- struct{}{}
}

//
//func (mfh *markerFileHandler) markSegmentAsync() {
//	for {
//		select {
//		case segmentToMark := <-mfh.segmentToMark:
//			fmt.Println("got message to mark a file: ", segmentToMark)
//			if segmentToMark >= 0 {
//				var (
//					segmentText = strconv.Itoa(segmentToMark)
//					tmp         = mfh.lastMarkedSegmentFilePath + ".tmp"
//				)
//
//				if err := os.WriteFile(tmp, []byte(segmentText), 0o666); err != nil {
//					fmt.Println("error: ", err)
//					level.Error(mfh.logger).Log("msg", "could not create segment marker file", "file", tmp, "err", err)
//					return
//				}
//				if err := fileutil.Replace(tmp, mfh.lastMarkedSegmentFilePath); err != nil {
//					level.Error(mfh.logger).Log("msg", "could not replace segment marker file", "file", mfh.lastMarkedSegmentFilePath, "err", err)
//					return
//				}
//
//				level.Debug(mfh.logger).Log("msg", "updated segment marker file", "file", mfh.lastMarkedSegmentFilePath, "segment", segmentToMark)
//			}
//		case <-mfh.quit:
//			level.Debug(mfh.logger).Log("msg", "quitting marker handler")
//			return
//		}
//	}
//}
