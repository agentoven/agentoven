// Package rag provides the RAG (Retrieval-Augmented Generation) pipeline engine.
// Supports 5 strategies: naive, sentence-window, parent-document, HyDE, agentic.
package rag

import (
	"strings"
	"unicode/utf8"
)

// ChunkerConfig configures the text chunker.
type ChunkerConfig struct {
	ChunkSize    int    // Target chunk size in characters (default 512)
	ChunkOverlap int    // Overlap between chunks (default 50)
	Separator    string // Separator to split on (default "\n\n")
	Passthrough  bool   // If true, return the entire text as one chunk
}

// DefaultChunkerConfig returns sensible defaults for recursive text splitting.
func DefaultChunkerConfig() ChunkerConfig {
	return ChunkerConfig{
		ChunkSize:    512,
		ChunkOverlap: 50,
		Separator:    "\n\n",
	}
}

// Chunk holds a single chunk of text with its position.
type Chunk struct {
	Text     string            `json:"text"`
	Index    int               `json:"index"`    // 0-based chunk index
	Metadata map[string]string `json:"metadata"` // inherited from parent + chunk-specific
}

// ChunkText splits text into overlapping chunks using recursive splitting.
// Supports passthrough mode (returns entire text as single chunk).
func ChunkText(text string, config ChunkerConfig) []Chunk {
	if config.ChunkSize <= 0 {
		config.ChunkSize = 512
	}
	if config.ChunkOverlap < 0 {
		config.ChunkOverlap = 0
	}

	// Passthrough: return entire text as one chunk
	if config.Passthrough || utf8.RuneCountInString(text) <= config.ChunkSize {
		return []Chunk{{Text: text, Index: 0, Metadata: map[string]string{}}}
	}

	// Recursive splitting: try separators in order of priority
	separators := []string{"\n\n", "\n", ". ", " ", ""}
	if config.Separator != "" {
		separators = append([]string{config.Separator}, separators...)
	}

	return recursiveSplit(text, separators, config.ChunkSize, config.ChunkOverlap)
}

// recursiveSplit splits text recursively trying each separator.
func recursiveSplit(text string, separators []string, chunkSize, overlap int) []Chunk {
	if utf8.RuneCountInString(text) <= chunkSize {
		return []Chunk{{Text: text, Metadata: map[string]string{}}}
	}

	// Find the best separator (first one that produces segments)
	var segments []string
	var usedSep string
	for _, sep := range separators {
		if sep == "" {
			// Character-level split
			segments = splitByRunes(text, chunkSize)
			usedSep = ""
			break
		}
		parts := strings.Split(text, sep)
		if len(parts) > 1 {
			segments = parts
			usedSep = sep
			break
		}
	}

	if len(segments) == 0 {
		return []Chunk{{Text: text, Metadata: map[string]string{}}}
	}

	// Merge segments into chunks of target size
	var chunks []Chunk
	var current strings.Builder
	for _, seg := range segments {
		candidate := current.String()
		if candidate != "" {
			candidate += usedSep
		}
		candidate += seg

		if utf8.RuneCountInString(candidate) > chunkSize && current.Len() > 0 {
			// Flush current chunk
			chunks = append(chunks, Chunk{Text: current.String(), Metadata: map[string]string{}})

			// Apply overlap: keep the tail of the current chunk
			tail := overlapTail(current.String(), overlap)
			current.Reset()
			if tail != "" {
				current.WriteString(tail)
				current.WriteString(usedSep)
			}
			current.WriteString(seg)
		} else {
			if current.Len() > 0 {
				current.WriteString(usedSep)
			}
			current.WriteString(seg)
		}
	}
	if current.Len() > 0 {
		chunks = append(chunks, Chunk{Text: current.String(), Metadata: map[string]string{}})
	}

	// Set indices
	for i := range chunks {
		chunks[i].Index = i
	}
	return chunks
}

// overlapTail returns the last `n` characters of s.
func overlapTail(s string, n int) string {
	runes := []rune(s)
	if n >= len(runes) {
		return s
	}
	return string(runes[len(runes)-n:])
}

// splitByRunes splits text into segments of n runes each.
func splitByRunes(text string, n int) []string {
	runes := []rune(text)
	var segments []string
	for i := 0; i < len(runes); i += n {
		end := i + n
		if end > len(runes) {
			end = len(runes)
		}
		segments = append(segments, string(runes[i:end]))
	}
	return segments
}
