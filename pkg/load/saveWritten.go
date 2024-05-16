package load

import (
	"fmt"
	"os"
	"strconv"
)

type saveWrittenDigestsAction struct {
	filename string
}

func (self *saveWrittenDigestsAction) RunAction(actionContext *ActionContext) error {
	f, err := os.Create(self.filename)
	if err != nil {
		return fmt.Errorf("failed to save read digests to file %s: %s", self.filename, err)
	}
	defer f.Close()

	f.WriteString(fmt.Sprintf("hash,size_bytes,duration_ms,cache_type,present\n"))
	for _, writtenDigest := range actionContext.WrittenDigests {
		cacheType := "bytestream"
		if !writtenDigest.usedBytestream {
			cacheType = "cas"
		}
		line := fmt.Sprintf("%s,%d,%d,%s,%s\n", writtenDigest.digest.Hash, writtenDigest.digest.SizeBytes, writtenDigest.duration.Milliseconds(), cacheType, strconv.FormatBool(writtenDigest.present))
		_, err = f.WriteString(line)
		if err != nil {
			return fmt.Errorf("failed to write to file: %s", err)
		}
	}
	return nil
}

func ParseSaveWrittenDigestsAction(args []string) (Action, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("filename to save to must be specified")
	}

	action := saveWrittenDigestsAction{
		filename: args[0],
	}

	return &action, nil
}
