// Copyright 2021 Toolchain Labs, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package load

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"strconv"
	"time"

	"github.com/toolchainlabs/remote-api-tools/pkg/casutil"
	"github.com/toolchainlabs/remote-api-tools/pkg/retry"
	"github.com/toolchainlabs/remote-api-tools/pkg/stats"
	remote_pb "github.com/toolchainlabs/remote-api-tools/protos/build/bazel/remote/execution/v2"
)

type generateAction struct {
	numRequests int
	minBlobSize int
	maxBlobSize int
	concurrency int
}

func (g *generateAction) String() string {
	return fmt.Sprintf("%d:%d:%d", g.numRequests, g.minBlobSize, g.maxBlobSize)
}

type generateResult struct {
	startTime        time.Time
	endTime          time.Time
	success          int
	errors           int
	byteStreamWrites int
	blobWrites       int
}

type generateWorkItem struct {
	actionContext *ActionContext
	blob          []byte
}

type generateWorkResult struct {
	digest  *remote_pb.Digest
	err     error
	elapsed time.Duration
}

func generateWorker(workChan <-chan *generateWorkItem, resultChan chan<- *generateWorkResult) {
	log.Debug("generateWorker started")
	for wi := range workChan {
		startTime := time.Now()
		result := processWorkItem(wi)
		result.elapsed = time.Now().Sub(startTime)
		resultChan <- result
	}
	log.Debug("generateWorker stopped")
}

func writeBlobBatch(actionContext *ActionContext, data []byte) (*remote_pb.Digest, error) {
	digest, err := casutil.PutBytes(actionContext.Ctx, actionContext.CasClient, data, actionContext.InstanceName)
	if err != nil {
		return nil, fmt.Errorf("failed to write blob: %s", err)
	}
	return digest, nil
}

func writeBlobStream(actionContext *ActionContext, data []byte) (*remote_pb.Digest, error) {
	digest, err := casutil.PutBytesStream(actionContext.Ctx, actionContext.BytestreamClient, data, actionContext.WriteChunkSize,
		actionContext.InstanceName)
	if err != nil {
		return nil, fmt.Errorf("failed to write blob (bytestream) %s", err)
	}
	return digest, nil
}

func processWorkItem(wi *generateWorkItem) *generateWorkResult {
	var digest *remote_pb.Digest
	var err error
	blobSize := len(wi.blob)
	if int64(blobSize) < wi.actionContext.MaxBatchBlobSize {
		digest, err = writeBlobBatch(wi.actionContext, wi.blob)
	} else {
		digest, err = writeBlobStream(wi.actionContext, wi.blob)
	}
	if err != nil {
		return &generateWorkResult{
			digest: digest,
			err:    err,
		}
	}

	_, err = retry.ExpBackoff(6, time.Duration(250)*time.Millisecond, time.Duration(5)*time.Second, func() (interface{}, error) {
		missingBlobs, err := casutil.FindMissingBlobs(wi.actionContext.Ctx, wi.actionContext.CasClient, []*remote_pb.Digest{digest},
			wi.actionContext.InstanceName)
		if err != nil {
			return nil, err
		}
		if len(missingBlobs) > 0 {
			return nil, fmt.Errorf("just-written blob is reported as not present in the CAS")
		}
		return nil, nil
	}, func(err error) bool {
		return true
	})
	if err != nil {
		return &generateWorkResult{
			digest: digest,
			err:    fmt.Errorf("failed to verify existence of blob: %s", err),
		}
	}

	wi.actionContext.AddKnownDigest(digest, true)

	return &generateWorkResult{
		digest: digest,
		err:    nil,
	}
}

func (g *generateAction) RunAction(actionContext *ActionContext) error {
	result := generateResult{
		startTime: time.Now(),
	}

	workChan := make(chan *generateWorkItem)
	resultChan := make(chan *generateWorkResult)

	for c := 0; c < g.concurrency; c++ {
		go generateWorker(workChan, resultChan)
	}

	var workItems []*generateWorkItem

	for i := 0; i < g.numRequests; i++ {
		blobSize := actionContext.RandGen.Intn(g.maxBlobSize-g.minBlobSize) + g.minBlobSize
		wi := generateWorkItem{
			actionContext: actionContext,
			blob:          make([]byte, blobSize),
		}
		n, err := actionContext.RandGen.Read(wi.blob)
		if err != nil {
			log.Errorf("rand failed: %s", err)
			return err
		}
		if n != blobSize {
			log.Errorf("rand gave less than expected")
			return err
		}

		workItems = append(workItems, &wi)
	}

	// Inject the work items into the channel.
	go func() {
		for _, workItem := range workItems {
			workChan <- workItem
		}

		close(workChan)
	}()

	elapsedTimes := make([]time.Duration, g.numRequests)

	for i := 0; i < g.numRequests; i++ {
		r := <-resultChan
		if r.err == nil {
			result.success += 1
		} else {
			result.errors += 1
			log.WithFields(log.Fields{
				"size": r.digest.SizeBytes,
				"err":  r.err.Error(),
			}).Error("request error")
		}
		elapsedTimes[i] = r.elapsed
		usedBytestream := true
		if int64(r.digest.SizeBytes) < actionContext.MaxBatchBlobSize {
			result.blobWrites += 1
			usedBytestream = false
		} else {
			result.byteStreamWrites += 1
		}
		actionContext.AddWrittenDigest(r.digest, usedBytestream, r.elapsed, true)

		if i%100 == 0 {
			log.Debugf("progress: %d / %d", i, g.numRequests)
		}
	}

	result.endTime = time.Now()

	close(resultChan)

	fmt.Printf("program: %s\n  randSeed: %d\n  startTime: %s\n  endTime: %s\n  success: %d\n  errors: %d\n  byteStreamWrites: %d\n  blobWrites: %d\n",
		g.String(),
		actionContext.RandSeed,
		result.startTime.String(),
		result.endTime.String(),
		result.success,
		result.errors,
		result.byteStreamWrites,
		result.blobWrites,
	)

	stats.PrintTimingStats(elapsedTimes)

	return nil
}

func ParseGenerateAction(args []string) (Action, error) {
	if len(args) < 3 || len(args) > 4 {
		return nil, fmt.Errorf("unable to parse program: expected 3-4 fields, got %d fields", len(args))
	}

	numRequests, err := strconv.Atoi(args[0])
	if err != nil {
		return nil, fmt.Errorf("unable to parse number of requests: %s: %s", args[0], err)
	}

	minBlobSize, err := strconv.Atoi(args[1])
	if err != nil {
		return nil, fmt.Errorf("unable to parse min blob size: %s: %s", args[1], err)
	}

	maxBlobSize, err := strconv.Atoi(args[2])
	if err != nil {
		return nil, fmt.Errorf("unable to parse max blob size: %s: %s", args[2], err)
	}

	concurrency := 50
	if len(args) >= 4 {
		c, err := strconv.Atoi(args[3])
		if err != nil {
			return nil, fmt.Errorf("unable to parse concurrency: %s: %s", args[3], err)
		}
		concurrency = c
	}

	action := generateAction{
		numRequests: numRequests,
		minBlobSize: minBlobSize,
		maxBlobSize: maxBlobSize,
		concurrency: concurrency,
	}

	return &action, nil
}
