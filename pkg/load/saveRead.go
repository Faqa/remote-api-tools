package load

import (
	"fmt"
	"os"
)

type saveReadDigestsAction struct {
	filename string
}

func (self *saveReadDigestsAction) RunAction(actionContext *ActionContext) error {
	f, err := os.Create(self.filename)
	if err != nil {
		return fmt.Errorf("failed to save read digests to file %s: %s", self.filename, err)
	}
	defer f.Close()

	f.WriteString(fmt.Sprintf("hash,size_bytes,duration_ms,cache_type\n"))
	for _, readDigest := range actionContext.ReadDigests {
		cacheType := "bytestream"
		if !readDigest.usedBytestream {
			cacheType = "cas"
		}
		line := fmt.Sprintf("%s,%d,%d,%s\n", readDigest.digest.Hash, readDigest.digest.SizeBytes, readDigest.duration.Milliseconds(), cacheType)
		_, err = f.WriteString(line)
		if err != nil {
			return fmt.Errorf("failed to write to file: %s", err)
		}
	}
	return nil
}

func ParseSaveReadDigestsAction(args []string) (Action, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("filename to save to must be specified")
	}

	action := saveReadDigestsAction{
		filename: args[0],
	}

	return &action, nil
}
