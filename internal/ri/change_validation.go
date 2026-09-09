package ri

import (
	"errors"
	"strings"
)

func validateChangeValues(producers []string, changes []SourceChange) error {
	previous := ""
	for _, producer := range producers {
		if producer <= previous || len(producer) > 256 {
			return errors.New("invalid changed producer ordering")
		}
		previous = producer
	}
	for _, change := range changes {
		if len(change.Producer) > 256 || len(change.Path) > 4096 {
			return errors.New("source change label exceeds bounds")
		}
		for _, hash := range []*string{change.Before, change.After} {
			if hash != nil && (len(*hash) != 64 || strings.Trim(*hash, "0123456789abcdef") != "") {
				return errors.New("invalid source change digest")
			}
		}
	}
	return nil
}
