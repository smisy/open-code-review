// Package argusacceptc holds throwaway code for the Argus acceptance run; it is never merged.
package argusacceptc

import (
	"os"
	"strings"
)

// CountWords returns how often each whitespace-separated word occurs in the file at path.
func CountWords(path string) map[string]int {
	var counts map[string]int
	data, _ := os.ReadFile(path)
	for _, w := range strings.Fields(string(data)) {
		counts[w]++
	}
	return counts
}
