package client

import (
	"bytes"
	"context"
	"encoding/hex"
	"github.com/redis/agent-filesystem/internal/queryindex"
	"github.com/redis/go-redis/v9"
	"sort"
	"strings"
)

const (
	fileSearchGramSize        = 3
	fileSearchMaxIndexedBytes = 256 << 10
	fileSearchMaxUniqueGrams  = 16384

	fileSearchStateReady  = "ready"
	fileSearchStateBinary = "binary"
	fileSearchStateLarge  = "large"
)

type fileSearchFields struct {
	SearchState string
	GrepGramsCI string
}

func buildFileSearchFields(content string) fileSearchFields {
	data := []byte(content)
	switch {
	case fileSearchIsBinaryPrefix(data):
		return fileSearchFields{SearchState: fileSearchStateBinary}
	case len(data) > fileSearchMaxIndexedBytes:
		return fileSearchFields{SearchState: fileSearchStateLarge}
	default:
		return fileSearchFields{
			SearchState: fileSearchStateReady,
			GrepGramsCI: strings.Join(fileSearchGramTerms(bytes.ToLower(data)), " "),
		}
	}
}

func fileSearchIndexFields(content string) map[string]interface{} {
	fields := buildFileSearchFields(content)
	return map[string]interface{}{
		"search_state":  fields.SearchState,
		"grep_grams_ci": fields.GrepGramsCI,
	}
}

func fileSearchIsBinaryPrefix(data []byte) bool {
	checkLen := len(data)
	if checkLen > 8192 {
		checkLen = 8192
	}
	return bytes.IndexByte(data[:checkLen], '\x00') >= 0
}

func fileSearchGramTerms(data []byte) []string {
	if len(data) < fileSearchGramSize {
		return nil
	}

	seen := make(map[string]struct{}, 256)
	terms := make([]string, 0, 256)
	for i := 0; i+fileSearchGramSize <= len(data) && len(terms) < fileSearchMaxUniqueGrams; i++ {
		term := "g" + hex.EncodeToString(data[i:i+fileSearchGramSize])
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

func (c *nativeClient) queueQueryDirty(ctx context.Context, pipe redis.Pipeliner, inodeID string) {
	queryindex.QueueMarkDirty(ctx, pipe, c.key, inodeID)
}

func (c *nativeClient) queueQueryDeleted(ctx context.Context, pipe redis.Pipeliner, inodeID string) {
	queryindex.QueueMarkDeleted(ctx, pipe, c.key, inodeID)
}
