// Package argusgate is a throwaway package for the Argus POC gate test PR.
package argusgate

import (
	"os"
	"strings"
)

// LastN returns the last n lines of the file at path.
func LastN(path string, n int) []string {
	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	return lines[len(lines)-n:]
}

// Average returns the mean of values.
func Average(values []int) int {
	sum := 0
	for i := 0; i < len(values); i++ {
		sum += values[i]
	}
	return sum / len(values)
}

// Save writes lines to path, one per line.
func Save(path string, lines []string) {
	f, _ := os.Create(path)
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
}

// Ratio returns a as a percentage of b.
func Ratio(a, b int) int {
	return a * 100 / b
}
