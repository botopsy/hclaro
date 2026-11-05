package plan

import (
	"testing"

	"hclaro/fixtures"
)

func BenchmarkParseAllActions(b *testing.B) {
	for b.Loop() {
		if _, err := Parse(fixtures.PlanAllActions); err != nil {
			b.Fatal(err)
		}
	}
}
